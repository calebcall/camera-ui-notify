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

// discord implements Backend for a Discord incoming webhook
// (https://discord.com/developers/docs/resources/webhook#execute-webhook).
// Delivery is a single HTTP POST to the user-provided webhook URL — unlike
// ntfy/gotify/pushover/telegram there is no fixed hosted endpoint to make
// injectable, so tests point cfg["webhook"] directly at an httptest server.
type discord struct {
	// client performs the HTTP request. Defaulted by newDiscord; overridable
	// in tests so Send never touches the real network.
	client *http.Client
}

// newDiscord constructs a Discord backend with a client suitable for
// production use (~10s timeout). Tests override the client field to point
// at an httptest server.
func newDiscord() *discord {
	return &discord{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func init() {
	Register(newDiscord())
}

func (d *discord) ID() string    { return "discord" }
func (d *discord) Label() string { return "Discord" }

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

	return map[string]string{
		"webhook": webhook,
	}, nil
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

// discordWebhookPayload is the JSON body posted to the webhook URL.
type discordWebhookPayload struct {
	Embeds []discordEmbed `json:"embeds"`
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
	return embed
}

// Send delivers a single notification to a Discord incoming webhook.
// Discord returns 204 on success; the generic 200-299 = OK check below
// already covers that.
func (d *discord) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	webhookURL := cfg["webhook"]
	embed := discordEmbedFor(notif)

	if ctx == nil {
		ctx = context.Background()
	}

	var req *http.Request
	var err error
	if len(notif.Thumbnail) > 0 {
		embed.Image = &discordEmbedImage{URL: "attachment://snapshot.jpg"}
		req, err = newDiscordMultipartRequest(ctx, webhookURL, embed, notif.Thumbnail)
	} else {
		payload := discordWebhookPayload{Embeds: []discordEmbed{embed}}
		var buf []byte
		buf, err = json.Marshal(payload)
		if err == nil {
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(buf))
			if err == nil {
				req.Header.Set("Content-Type", "application/json")
			}
		}
	}
	if err != nil {
		return fmt.Errorf("discord: build request: %w", err)
	}

	client := d.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("discord: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxBody = 512
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("discord: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}

// newDiscordMultipartRequest builds a multipart/form-data POST carrying
// payload_json (the embeds, with the embed's image pointed at
// attachment://snapshot.jpg) plus the thumbnail bytes as a "files[0]" file
// part (filename snapshot.jpg, Content-Type image/jpeg), per
// https://discord.com/developers/docs/resources/webhook#execute-webhook.
func newDiscordMultipartRequest(ctx context.Context, targetURL string, embed discordEmbed, thumbnail []byte) (*http.Request, error) {
	payload := discordWebhookPayload{Embeds: []discordEmbed{embed}}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if err := mw.WriteField("payload_json", string(payloadJSON)); err != nil {
		return nil, err
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="files[0]"; filename="snapshot.jpg"`)
	h.Set("Content-Type", "image/jpeg")
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(thumbnail); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, nil
}
