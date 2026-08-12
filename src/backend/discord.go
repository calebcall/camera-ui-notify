package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// Discord embed colors keyed by notif.Severity, per the brief: Info is
// blue, Warn is yellow, Error/Critical are red.
const (
	discordColorInfo  = 0x3498db
	discordColorWarn  = 0xf1c40f
	discordColorError = 0xe74c3c
)

// discordSuppressNotifications is Discord's SUPPRESS_NOTIFICATIONS message
// flag (1 << 12): the message still appears in the channel but sends no push.
// See https://discord.com/developers/docs/resources/message#message-object-message-flags.
const discordSuppressNotifications = 1 << 12

// discord implements Backend for a Discord incoming webhook
// (https://discord.com/developers/docs/resources/webhook#execute-webhook).
// Delivery is a single HTTP POST to the user-provided webhook URL — unlike
// ntfy/gotify/pushover/telegram there is no fixed hosted endpoint to make
// injectable, so tests point cfg["webhook"] directly at an httptest server.
type discord struct {
	// client performs the HTTP request. Defaulted by newDiscord; overridable
	// in tests so Send never touches the real network.
	client *http.Client
	// collapse remembers the message id delivered under each notification Tag
	// so a later same-tag publish edits that message instead of posting a
	// second one. Nil disables collapsing (see collapseStore).
	collapse *collapseStore
}

// newDiscord constructs a Discord backend with a client suitable for
// production use (~10s timeout). Tests override the client field to point
// at an httptest server.
func newDiscord() *discord {
	return &discord{
		client:   &http.Client{Timeout: 10 * time.Second},
		collapse: newCollapseStore(),
	}
}

func init() {
	Register(newDiscord())
}

func (d *discord) ID() string    { return "discord" }
func (d *discord) Label() string { return "Discord" }

// ReplacesTaggedMessages implements TagReplacer: Send edits the message it
// previously delivered under the same tag via PATCH .../messages/{id}.
func (d *discord) ReplacesTaggedMessages() bool { return true }

func (d *discord) Schema() []sdk.JsonSchema {
	cond := []sdk.SchemaCondition{{Key: "service", Value: "discord"}}
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "discord_webhook",
			Title:       "Webhook URL",
			Description: "The Discord incoming webhook URL to post to.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   cond,
		},
		clipUploadSchema("discord_clip",
			"Attach the recording to the message as a playable video instead of a \"Play clip\" link. "+
				"The snapshot is kept alongside it. Every clip is downloaded from camera.ui and "+
				"re-uploaded to Discord — and again when the AI description follows — so anything over "+
				"8 MB, or an upload Discord rejects, falls back to the link.",
			cond),
	}
}

// ParseTarget validates the raw registration input and returns the
// normalized config persisted in NotifierDevice.Metadata. Schema field keys
// are namespaced (discord_*) so they don't collide with other backends'
// fields in the flattened NotificationSettings() form; the returned cfg
// uses the short keys Send reads.
func (d *discord) ParseTarget(input map[string]any) (map[string]string, error) {
	webhook, _ := input["discord_webhook"].(string)
	webhook = strings.TrimSpace(webhook)
	if webhook == "" {
		return nil, errors.New("discord: webhook is required")
	}

	cfg := map[string]string{
		"webhook": webhook,
	}
	if parseClipOptIn(input, "discord_clip") {
		cfg["clip"] = clipOptIn
	}
	return cfg, nil
}

// discordMaxClipBytes caps an uploaded clip at 8 MiB. Discord's own limit for
// a webhook upload is 10 MiB on an unboosted server and applies to the whole
// request, so the headroom leaves room for the snapshot travelling in the
// same multipart body. A clip past this is not fetched at all — Discord would
// reject the upload after the whole download had already been paid for.
const discordMaxClipBytes = 8 << 20

// ClipLimit implements ClipUploader. Unlike Telegram there is no cheap edit
// here: Discord's edit endpoint drops any attachment the request does not
// re-send, so a silent follow-up has to carry the clip again to keep it.
func (d *discord) ClipLimit(cfg map[string]string, notif sdk.Notification) int {
	if cfg["clip"] != clipOptIn {
		return 0
	}
	return discordMaxClipBytes
}

// discordColorForSeverity maps notif.Severity onto a Discord embed color.
func discordColorForSeverity(sev sdk.Severity) int {
	switch sev {
	case sdk.SeverityWarn:
		return discordColorWarn
	case sdk.SeverityError, sdk.SeverityCritical:
		return discordColorError
	default:
		return discordColorInfo
	}
}

// discordEmbedImage is the "image" object attached to an embed when a
// Thumbnail is delivered alongside it.
type discordEmbedImage struct {
	URL string `json:"url"`
}

// discordEmbed is a single Discord message embed.
type discordEmbed struct {
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	Color       int                `json:"color"`
	URL         string             `json:"url,omitempty"`
	Image       *discordEmbedImage `json:"image,omitempty"`
}

// discordAttachment describes a file part in an edit payload. Discord's
// message-edit endpoint treats the attachments array as the complete list to
// keep: omit it on an edit and every existing attachment is dropped, so an
// edit that re-uploads files must name each one here (id N = files[N]) and
// one that carries none must send an explicit empty array.
type discordAttachment struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
}

// discordFile is one file part of a webhook request. The order of a
// []discordFile is significant: it fixes both the files[N] part names and
// the ids in the edit payload's attachments array, so the two cannot drift.
type discordFile struct {
	filename    string
	contentType string
	data        []byte
}

// discordFilesFor assembles the file parts for one delivery: the snapshot
// first (so the embed's attachment://snapshot.jpg reference and files[0] line
// up as they always have), then the clip when the target opted into
// uploading it.
func discordFilesFor(thumbnail, clip []byte) []discordFile {
	var files []discordFile
	if len(thumbnail) > 0 {
		files = append(files, discordFile{filename: "snapshot.jpg", contentType: "image/jpeg", data: thumbnail})
	}
	if len(clip) > 0 {
		files = append(files, discordFile{filename: "clip.mp4", contentType: "video/mp4", data: clip})
	}
	return files
}

// discordWebhookPayload is the JSON body posted to the webhook URL (and,
// for a replacement, PATCHed to .../messages/{id}).
type discordWebhookPayload struct {
	Embeds []discordEmbed `json:"embeds"`
	// Flags carries SUPPRESS_NOTIFICATIONS for a silent publish. Omitted when
	// zero; Discord ignores it on edits, which never re-notify anyway.
	Flags int `json:"flags,omitempty"`
	// Attachments is nil on an initial post (Discord infers the file parts)
	// and non-nil — possibly an empty array — on an edit. A pointer so an
	// empty list still marshals, since that is what clears an old image.
	Attachments *[]discordAttachment `json:"attachments,omitempty"`
}

// discordEmbedFor builds the single embed used for every notification:
// title, body as description, a severity-derived color, and — when
// DeepLink is absolute — a url (which makes the embed title a clickable
// link).
func discordEmbedFor(notif sdk.Notification) discordEmbed {
	embed := discordEmbed{
		Title:       notif.Title,
		Description: notif.Body,
		Color:       discordColorForSeverity(notif.Severity),
	}
	if strings.HasPrefix(notif.DeepLink, "http") {
		embed.URL = notif.DeepLink
	}
	// An embed's own url is already the deep link, and Discord ignores the
	// "video" object on a webhook embed (it only ever fills that one itself,
	// for providers it recognises), so the clip is offered as a masked link
	// at the end of the description. The target is wrapped in <> so a URL
	// containing a closing paren — a signed query string, say — still parses
	// as one link.
	if link := VideoLink(notif); link != "" {
		if embed.Description != "" {
			embed.Description += "\n\n"
		}
		embed.Description += "[" + VideoLinkLabel + "](<" + link + ">)"
	}
	return embed
}

// Send delivers a single notification to a Discord incoming webhook.
// Discord returns 204 on success; the generic 200-299 = OK check below
// already covers that.
//
// When notif carries a Tag this backend has already delivered to the same
// webhook, the earlier message is edited in place rather than a second one
// posted — that is how the AI description arriving after a detection alert
// updates the alert instead of duplicating it. An edit Discord rejects (the
// message was deleted, ...) falls back to a fresh post, so a failed
// replacement still delivers the content.
func (d *discord) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	return d.send(ctx, cfg, notif, nil)
}

// SendWithClip implements ClipUploader: the same delivery as Send, with the
// clip uploaded as a second attachment that Discord renders as a player.
// Unlike Telegram, Discord takes any number of files per message, so the
// snapshot stays in the embed alongside it.
func (d *discord) SendWithClip(ctx context.Context, cfg map[string]string, notif sdk.Notification, clip []byte) error {
	return d.send(ctx, cfg, notif, clip)
}

func (d *discord) send(ctx context.Context, cfg map[string]string, notif sdk.Notification, clip []byte) error {
	webhookURL := cfg["webhook"]
	// With the clip attached to the message, the "Play clip" link in the
	// description would only point at what is already there. Stripped before
	// the embed is built so the edit path below renders identically.
	if len(clip) > 0 {
		notif = WithoutVideoLink(notif)
	}
	embed := discordEmbedFor(notif)
	thumbnail := notif.Thumbnail
	if len(thumbnail) > 0 {
		embed.Image = &discordEmbedImage{URL: "attachment://snapshot.jpg"}
	}
	files := discordFilesFor(thumbnail, clip)

	if ctx == nil {
		ctx = context.Background()
	}

	key := ""
	if notif.Tag != "" {
		key = collapseKey("discord", webhookURL, notif.Tag)
	}

	// Only a Silent publish replaces: that flag is camera.ui saying "this
	// supersedes the one I already sent". A loud publish reusing the tag is a
	// genuinely new event (tags like "motion:cam-1" repeat), and rewriting the
	// previous alert would erase it from the channel's history.
	if SilentDelivery(notif) {
		if prev, ok := d.collapse.lookup(key); ok {
			if err := d.edit(ctx, webhookURL, prev.messageID, embed, files); err == nil {
				return nil
			}
			// The stored message is unusable; drop it so we don't retry the
			// same dead id on the next publish, and post a new message below.
			d.collapse.forget(key)
		}
	}

	payload := discordWebhookPayload{Embeds: []discordEmbed{embed}}
	if SilentDelivery(notif) {
		payload.Flags = discordSuppressNotifications
	}

	// Discord only returns the created message (and thus its id) when the
	// execute-webhook request asks for it, so opt in whenever we may want to
	// edit this message later.
	postURL := webhookURL
	if key != "" {
		var err error
		if postURL, err = discordWithWait(webhookURL); err != nil {
			return fmt.Errorf("discord: build request: %w", RedactRequestError(err))
		}
	}

	req, err := newDiscordRequest(ctx, http.MethodPost, postURL, payload, files)
	if err != nil {
		return fmt.Errorf("discord: build request: %w", RedactRequestError(err))
	}

	body, err := d.do(req)
	if err != nil {
		return err
	}

	if key != "" {
		var created struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body, &created) == nil && created.ID != "" {
			d.collapse.remember(key, created.ID, len(files) > 0)
		}
	}

	return nil
}

// edit replaces an already-delivered webhook message with notif's current
// embed, re-uploading every file it should still carry.
//
// Discord's edit endpoint keeps only what the attachments array names, so
// each file is re-sent rather than retained by id — which is also why a clip
// costs a second download and upload when the AI description follows its
// detection alert.
func (d *discord) edit(ctx context.Context, webhookURL, messageID string, embed discordEmbed, files []discordFile) error {
	attachments := []discordAttachment{}
	for i, f := range files {
		attachments = append(attachments, discordAttachment{ID: i, Filename: f.filename})
	}
	payload := discordWebhookPayload{
		Embeds:      []discordEmbed{embed},
		Attachments: &attachments,
	}

	editURL, err := discordMessageURL(webhookURL, messageID)
	if err != nil {
		return fmt.Errorf("discord: build edit request: %w", RedactRequestError(err))
	}

	req, err := newDiscordRequest(ctx, http.MethodPatch, editURL, payload, files)
	if err != nil {
		return fmt.Errorf("discord: build edit request: %w", RedactRequestError(err))
	}

	_, err = d.do(req)
	return err
}

// do performs req and returns the response body on a 2xx, or a non-nil error
// carrying Discord's complaint. The body is read (bounded) because the caller
// needs the created message's id out of it.
func (d *discord) do(req *http.Request) ([]byte, error) {
	client := d.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discord: request failed: %w", RedactRequestError(err))
	}
	defer resp.Body.Close()

	const maxBody = 4096
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxErrBody = 512
		if len(body) > maxErrBody {
			body = body[:maxErrBody]
		}
		return nil, fmt.Errorf("discord: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
}

// discordWithWait adds ?wait=true to a webhook URL, preserving any query the
// user already put on it (e.g. ?thread_id=).
func discordWithWait(webhookURL string) (string, error) {
	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("wait", "true")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// discordMessageURL builds the .../messages/{id} edit endpoint for a webhook,
// keeping any query the user put on the webhook URL (thread_id matters here)
// but dropping wait, which is not a parameter of the edit endpoint.
func discordMessageURL(webhookURL, messageID string) (string, error) {
	u, err := url.Parse(webhookURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Del("wait")
	u.RawQuery = q.Encode()
	u.Path = strings.TrimRight(u.Path, "/") + "/messages/" + url.PathEscape(messageID)
	return u.String(), nil
}

// newDiscordRequest builds either a plain JSON request or, when there are
// files to attach, the multipart/form-data equivalent carrying payload_json
// plus each file, for both the initial post and a later edit.
func newDiscordRequest(ctx context.Context, method, targetURL string, payload discordWebhookPayload, files []discordFile) (*http.Request, error) {
	if len(files) > 0 {
		return newDiscordMultipartRequest(ctx, method, targetURL, payload, files)
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// newDiscordMultipartRequest builds a multipart/form-data request carrying
// payload_json (the embeds, with the embed's image pointed at
// attachment://snapshot.jpg) plus each file as a "files[N]" part, per
// https://discord.com/developers/docs/resources/webhook#execute-webhook. N is
// the file's index in files, which is the id an edit's attachments array uses
// to refer to it.
func newDiscordMultipartRequest(ctx context.Context, method, targetURL string, payload discordWebhookPayload, files []discordFile) (*http.Request, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if err := mw.WriteField("payload_json", string(payloadJSON)); err != nil {
		return nil, err
	}

	for i, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition",
			`form-data; name="files[`+strconv.Itoa(i)+`]"; filename="`+f.filename+`"`)
		h.Set("Content-Type", f.contentType)
		part, err := mw.CreatePart(h)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, nil
}
