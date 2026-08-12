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
	"strconv"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// defaultTelegramBaseURL is the production Telegram Bot API base, used when
// baseURL is left at its zero value.
const defaultTelegramBaseURL = "https://api.telegram.org"

// telegram implements Backend for the Telegram Bot API
// (https://core.telegram.org/bots/api). Delivery is a single HTTP POST to
// /bot{token}/sendMessage (text only), /bot{token}/sendPhoto (an inline
// Thumbnail is present), or /bot{token}/sendVideo (the target opted into
// clip upload and the notification carries one — see ClipLimit).
type telegram struct {
	// client performs the HTTP request. Defaulted by newTelegram;
	// overridable in tests so Send never touches the real network.
	client *http.Client
	// baseURL is the Bot API base, e.g. "https://api.telegram.org". Defaulted
	// by newTelegram; overridable in tests to point at an httptest server.
	baseURL string
	// collapse remembers the message_id delivered under each notification Tag
	// so a later same-tag publish edits that message instead of posting a
	// second one. Nil disables collapsing (see collapseStore).
	collapse *collapseStore
}

// newTelegram constructs a Telegram backend with a client suitable for
// production use (~10s timeout) and the real Bot API base. Tests override
// the client and baseURL fields to point at an httptest server.
func newTelegram() *telegram {
	return &telegram{
		client:   &http.Client{Timeout: 10 * time.Second},
		baseURL:  defaultTelegramBaseURL,
		collapse: newCollapseStore(),
	}
}

func init() {
	Register(newTelegram())
}

func (tg *telegram) ID() string    { return "telegram" }
func (tg *telegram) Label() string { return "Telegram" }

// ReplacesTaggedMessages implements TagReplacer: Send edits the message it
// previously delivered under the same tag via editMessageText /
// editMessageCaption.
func (tg *telegram) ReplacesTaggedMessages() bool { return true }

func (tg *telegram) Schema() []sdk.JsonSchema {
	cond := []sdk.SchemaCondition{{Key: "service", Value: "telegram"}}
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "telegram_token",
			Title:       "Bot Token",
			Description: "The Telegram bot token issued by @BotFather.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "telegram_chat",
			Title:       "Chat ID",
			Description: "The Telegram chat (or channel) ID to deliver to.",
			Required:    true,
			Condition:   cond,
		},
		clipUploadSchema("telegram_clip",
			"Send the recording as a playable video (sendVideo) instead of a \"Play clip\" button. "+
				"Telegram carries one media item per message, so the clip replaces the snapshot. "+
				"Every clip is downloaded from camera.ui and re-uploaded to Telegram; anything over "+
				"50 MB, or an upload Telegram rejects, falls back to the button.",
			cond),
	}
}

// ParseTarget validates the raw registration input and returns the
// normalized config persisted in NotifierDevice.Metadata. Schema field keys
// are namespaced (telegram_*) so they don't collide with other backends'
// fields in the flattened NotificationSettings() form; the returned cfg
// uses the short keys Send reads.
func (tg *telegram) ParseTarget(input map[string]any) (map[string]string, error) {
	token, _ := input["telegram_token"].(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("telegram: token is required")
	}

	chat, _ := input["telegram_chat"].(string)
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, errors.New("telegram: chat is required")
	}

	cfg := map[string]string{
		"token": token,
		"chat":  chat,
	}
	if parseClipOptIn(input, "telegram_clip") {
		cfg["clip"] = clipOptIn
	}
	return cfg, nil
}

// telegramMaxClipBytes is the Bot API's upload ceiling for a file a bot
// sends (50 MB). A clip past it is not fetched at all — Telegram would
// reject the upload after the whole download had already been paid for.
const telegramMaxClipBytes = 50 << 20

// ClipLimit implements ClipUploader.
//
// It returns 0 for a silent follow-up that is going to be an edit:
// editMessageCaption revises the text of a message and leaves its media
// alone, so the clip already in the chat stays put and re-downloading it
// would buy nothing. (If that edit then fails and falls back to a fresh
// send, the replacement carries the clip as a button rather than a video —
// the same degradation a lost message id already causes.)
func (tg *telegram) ClipLimit(cfg map[string]string, notif sdk.Notification) int {
	if cfg["clip"] != clipOptIn {
		return 0
	}
	if SilentDelivery(notif) && notif.Tag != "" {
		if _, pending := tg.collapse.lookup(collapseKey("telegram", cfg["token"], cfg["chat"], notif.Tag)); pending {
			return 0
		}
	}
	return telegramMaxClipBytes
}

// telegramInlineButton is a single button in a Telegram inline keyboard.
type telegramInlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// telegramReplyMarkup wraps a Telegram inline keyboard.
type telegramReplyMarkup struct {
	InlineKeyboard [][]telegramInlineButton `json:"inline_keyboard"`
}

// telegramSendMessagePayload is the JSON body posted to sendMessage.
type telegramSendMessagePayload struct {
	ChatID              string               `json:"chat_id"`
	Text                string               `json:"text"`
	ReplyMarkup         *telegramReplyMarkup `json:"reply_markup,omitempty"`
	DisableNotification bool                 `json:"disable_notification,omitempty"`
}

// telegramEditPayload is the JSON body posted to editMessageText (Text set)
// or editMessageCaption (Caption set) to replace an already-delivered
// message. Telegram edits never re-alert the chat, so there is no
// disable_notification to send here.
type telegramEditPayload struct {
	ChatID      string               `json:"chat_id"`
	MessageID   string               `json:"message_id"`
	Text        string               `json:"text,omitempty"`
	Caption     string               `json:"caption,omitempty"`
	ReplyMarkup *telegramReplyMarkup `json:"reply_markup,omitempty"`
}

// telegramSendResponse is the slice of a Bot API send response this backend
// reads: the message_id needed to edit the message later.
type telegramSendResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int64 `json:"message_id"`
	} `json:"result"`
}

// telegramText builds the combined text used for both sendMessage's "text"
// and sendPhoto's "caption": the title, plus the body on its own line when
// non-empty.
func telegramText(notif sdk.Notification) string {
	if notif.Body == "" {
		return notif.Title
	}
	return notif.Title + "\n" + notif.Body
}

// telegramReplyMarkupFor builds the tap-through inline-keyboard reply markup
// for notif, or nil when it names nothing to open. The deep-link button's
// text comes from DeepLinkLabel so a non-detection notification (plugin
// update, system alert) isn't labelled "Open camera".
//
// A clip (camera.ui 2.1.6+) adds a second button on its own row. This is the
// default because Telegram carries one media item per message, so sending the
// clip as a video costs the snapshot; the "Upload video clips" toggle opts
// into paying that price, and when it is on the clip is already in the
// message and WithoutVideoLink has stripped it before this is called.
func telegramReplyMarkupFor(notif sdk.Notification) *telegramReplyMarkup {
	var rows [][]telegramInlineButton
	if strings.HasPrefix(notif.DeepLink, "http") {
		rows = append(rows, []telegramInlineButton{{Text: DeepLinkLabel(notif), URL: notif.DeepLink}})
	}
	if link := VideoLink(notif); link != "" {
		rows = append(rows, []telegramInlineButton{{Text: VideoLinkLabel, URL: link}})
	}
	if len(rows) == 0 {
		return nil
	}
	return &telegramReplyMarkup{InlineKeyboard: rows}
}

// Send delivers a single notification via the Telegram Bot API. Telegram
// has no severity/priority concept, so notif.Severity is not mapped to
// anything.
//
// When notif carries a Tag that this backend has already delivered to the
// same chat, the earlier message is edited in place rather than a second one
// posted — that is how the AI description arriving after a detection alert
// updates the alert instead of duplicating it. An edit that Telegram rejects
// (message deleted, too old, "message is not modified", ...) falls back to a
// fresh send, so a failed replacement still delivers the content.
func (tg *telegram) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	return tg.send(ctx, cfg, notif, nil)
}

// SendWithClip implements ClipUploader: the same delivery as Send, except
// the message is a sendVideo carrying the clip's bytes.
func (tg *telegram) SendWithClip(ctx context.Context, cfg map[string]string, notif sdk.Notification, clip []byte) error {
	return tg.send(ctx, cfg, notif, clip)
}

func (tg *telegram) send(ctx context.Context, cfg map[string]string, notif sdk.Notification, clip []byte) error {
	// With the clip in the message there is nothing for a "Play clip" button
	// to add, so it is dropped before any of the rendering helpers see the
	// notification — including on the edit path below, which must keep the
	// same markup the original message was posted with.
	if len(clip) > 0 {
		notif = WithoutVideoLink(notif)
	}

	baseURL := tg.baseURL
	if baseURL == "" {
		baseURL = defaultTelegramBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	token := cfg["token"]
	chat := cfg["chat"]

	if ctx == nil {
		ctx = context.Background()
	}

	key := ""
	if notif.Tag != "" {
		key = collapseKey("telegram", token, chat, notif.Tag)
	}

	// Only a Silent publish replaces: that flag is camera.ui saying "this
	// supersedes the one I already sent". A loud publish reusing the tag is a
	// genuinely new event (tags like "motion:cam-1" repeat), and rewriting the
	// previous alert would erase it from the chat's history.
	if SilentDelivery(notif) {
		if prev, ok := tg.collapse.lookup(key); ok {
			if err := tg.edit(ctx, baseURL, token, chat, notif, prev); err == nil {
				return nil
			}
			// The stored message is unusable; drop it so we don't retry the
			// same dead id on the next publish, and post a new message below.
			tg.collapse.forget(key)
		}
	}

	var req *http.Request
	var err error
	// Both a photo and a video are captioned media, which is the only
	// distinction the edit path cares about: either one rules out
	// editMessageText.
	media := len(notif.Thumbnail) > 0 || len(clip) > 0
	switch {
	case len(clip) > 0:
		req, err = tg.newSendVideoRequest(ctx, baseURL, token, chat, notif, clip)
	case len(notif.Thumbnail) > 0:
		req, err = tg.newSendPhotoRequest(ctx, baseURL, token, chat, notif)
	default:
		req, err = tg.newSendMessageRequest(ctx, baseURL, token, chat, notif)
	}
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", RedactRequestError(err))
	}

	body, err := tg.do(req)
	if err != nil {
		return err
	}

	if key != "" {
		var parsed telegramSendResponse
		if json.Unmarshal(body, &parsed) == nil && parsed.Result.MessageID != 0 {
			tg.collapse.remember(key, strconv.FormatInt(parsed.Result.MessageID, 10), media)
		}
	}

	return nil
}

// edit replaces an already-delivered message with notif's current content.
// A message posted as a photo can only have its caption edited (Telegram
// rejects editMessageText for it) and vice versa, which is why the delivered
// kind is recorded alongside the id.
func (tg *telegram) edit(ctx context.Context, baseURL, token, chat string, notif sdk.Notification, prev collapseEntry) error {
	payload := telegramEditPayload{
		ChatID:      chat,
		MessageID:   prev.messageID,
		ReplyMarkup: telegramReplyMarkupFor(notif),
	}
	method := "editMessageText"
	if prev.media {
		method = "editMessageCaption"
		payload.Caption = telegramText(notif)
	} else {
		payload.Text = telegramText(notif)
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram: encode edit payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/bot"+token+"/"+method, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("telegram: build edit request: %w", RedactRequestError(err))
	}
	req.Header.Set("Content-Type", "application/json")

	_, err = tg.do(req)
	return err
}

// do performs req and returns the response body on a 2xx, or a non-nil error
// carrying the server's complaint. The body is read (bounded) because the
// caller needs the message_id out of it.
func (tg *telegram) do(req *http.Request) ([]byte, error) {
	client := tg.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram: request failed: %w", RedactRequestError(err))
	}
	defer resp.Body.Close()

	const maxBody = 4096
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxErrBody = 512
		if len(body) > maxErrBody {
			body = body[:maxErrBody]
		}
		return nil, fmt.Errorf("telegram: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
}

// newSendMessageRequest builds the JSON sendMessage request used when there
// is no image to deliver.
func (tg *telegram) newSendMessageRequest(ctx context.Context, baseURL, token, chat string, notif sdk.Notification) (*http.Request, error) {
	payload := telegramSendMessagePayload{
		ChatID:              chat,
		Text:                telegramText(notif),
		ReplyMarkup:         telegramReplyMarkupFor(notif),
		DisableNotification: SilentDelivery(notif),
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}

	reqURL := baseURL + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// newSendPhotoRequest builds the multipart/form-data sendPhoto request used
// when notif.Thumbnail is present: the shared caption/markup fields plus a
// "photo" file part (filename snapshot.jpg, Content-Type image/jpeg).
func (tg *telegram) newSendPhotoRequest(ctx context.Context, baseURL, token, chat string, notif sdk.Notification) (*http.Request, error) {
	return newTelegramMediaRequest(ctx, baseURL+"/bot"+token+"/sendPhoto", chat, notif,
		telegramMediaPart{field: "photo", filename: "snapshot.jpg", contentType: "image/jpeg", data: notif.Thumbnail},
		nil)
}

// newSendVideoRequest builds the multipart/form-data sendVideo request used
// when the target has opted into clip upload: the same shared fields plus a
// "video" file part carrying the clip. supports_streaming lets Telegram play
// it inline while it downloads instead of making the user wait for the whole
// file.
//
// Telegram generates its own poster frame, so notif.Thumbnail is not sent:
// the Bot API's own "thumbnail" part is capped at 320px and 200 kB, which a
// camera snapshot will usually exceed.
func (tg *telegram) newSendVideoRequest(ctx context.Context, baseURL, token, chat string, notif sdk.Notification, clip []byte) (*http.Request, error) {
	return newTelegramMediaRequest(ctx, baseURL+"/bot"+token+"/sendVideo", chat, notif,
		telegramMediaPart{field: "video", filename: "clip.mp4", contentType: "video/mp4", data: clip},
		map[string]string{"supports_streaming": "true"})
}

// telegramMediaPart describes the one file part a captioned media request
// carries.
type telegramMediaPart struct {
	field       string
	filename    string
	contentType string
	data        []byte
}

// newTelegramMediaRequest builds a captioned-media multipart request
// (sendPhoto / sendVideo). Both carry the same envelope: chat_id, a
// "caption" field with the title/body text, disable_notification for a
// silent publish, and — when there is anything to link — a "reply_markup"
// field holding the inline keyboard as a JSON string (multipart form fields
// are always strings; the Bot API accepts reply_markup this way for
// non-JSON requests). extra carries whatever else the specific method wants.
func newTelegramMediaRequest(ctx context.Context, reqURL, chat string, notif sdk.Notification, media telegramMediaPart, extra map[string]string) (*http.Request, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if err := mw.WriteField("chat_id", chat); err != nil {
		return nil, err
	}
	if err := mw.WriteField("caption", telegramText(notif)); err != nil {
		return nil, err
	}
	if SilentDelivery(notif) {
		if err := mw.WriteField("disable_notification", "true"); err != nil {
			return nil, err
		}
	}
	if markup := telegramReplyMarkupFor(notif); markup != nil {
		markupJSON, err := json.Marshal(markup)
		if err != nil {
			return nil, err
		}
		if err := mw.WriteField("reply_markup", string(markupJSON)); err != nil {
			return nil, err
		}
	}
	for k, v := range extra {
		if err := mw.WriteField(k, v); err != nil {
			return nil, err
		}
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="`+media.field+`"; filename="`+media.filename+`"`)
	h.Set("Content-Type", media.contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(media.data); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, nil
}
