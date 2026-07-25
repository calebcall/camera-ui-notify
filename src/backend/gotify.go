package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// gotify implements Backend for a self-hosted Gotify server
// (https://gotify.net/docs/pushmsg). Delivery is a single HTTP POST to the
// server's /message endpoint, authenticated via an application token sent
// in the X-Gotify-Key request header (rather than a ?token= query
// parameter) so the token never appears in a *url.Error message logged on
// transport failure.
type gotify struct {
	// client performs the HTTP request. Defaulted by newGotify; overridable
	// in tests so Send never touches the real network.
	client *http.Client
}

// newGotify constructs a Gotify backend with a client suitable for
// production use (~10s timeout). Tests override the client field to point
// at an httptest server.
func newGotify() *gotify {
	return &gotify{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func init() {
	Register(newGotify())
}

func (g *gotify) ID() string    { return "gotify" }
func (g *gotify) Label() string { return "Gotify" }

func (g *gotify) Schema() []sdk.JsonSchema {
	cond := []sdk.SchemaCondition{{Key: "service", Value: "gotify"}}
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "gotify_server",
			Title:       "Server",
			Description: "Base URL of the Gotify server.",
			Required:    true,
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "gotify_token",
			Title:       "Application token",
			Description: "Gotify application token used to authenticate published messages.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   cond,
		},
	}
}

// ParseTarget validates the raw registration input and returns the
// normalized config persisted in NotifierDevice.Metadata. Schema field keys
// are namespaced (gotify_*) so they don't collide with other backends'
// fields in the flattened NotificationSettings() form; the returned cfg
// uses the short keys Send reads.
func (g *gotify) ParseTarget(input map[string]any) (map[string]string, error) {
	server, _ := input["gotify_server"].(string)
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, errors.New("gotify: server is required")
	}
	server = strings.TrimRight(server, "/")

	token, _ := input["gotify_token"].(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("gotify: token is required")
	}

	return map[string]string{
		"server": server,
		"token":  token,
	}, nil
}

// gotifyPayload is the JSON body posted to a Gotify server's /message
// endpoint. See https://gotify.net/docs/pushmsg.
type gotifyPayload struct {
	Title    string        `json:"title"`
	Message  string        `json:"message"`
	Priority int           `json:"priority"`
	Extras   *gotifyExtras `json:"extras,omitempty"`
}

type gotifyExtras struct {
	ClientDisplay      *gotifyClientDisplay      `json:"client::display,omitempty"`
	ClientNotification *gotifyClientNotification `json:"client::notification,omitempty"`
}

type gotifyClientDisplay struct {
	ContentType string `json:"contentType"`
}

type gotifyClientNotification struct {
	Click       *gotifyClick `json:"click,omitempty"`
	BigImageURL string       `json:"bigImageUrl,omitempty"`
}

type gotifyClick struct {
	URL string `json:"url"`
}

// Send delivers a single notification via a Gotify publish request.
func (g *gotify) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	server := cfg["server"]
	token := cfg["token"]

	body := notif.Body
	if body == "" {
		body = notif.Title
	}

	payload := gotifyPayload{
		Title:   notif.Title,
		Message: body,
		// Gotify treats priority 0-3 as "silent" (no system notification, only
		// an in-app list entry), so mapping Info->0 makes detections appear to
		// deliver nothing. Map into 4..10 so every severity raises a real
		// notification: Info=4, Warn=6, Error=8, Critical=10.
		Priority: PriorityScale(notif.Severity, 4, 10),
	}

	// Inline notif.Thumbnail bytes aren't supported here: Gotify's
	// client::notification.bigImageUrl extra needs a hosted URL, not raw
	// bytes, so only notif.ImageURL can be forwarded as an image.

	if notif.DeepLink != "" || notif.ImageURL != "" {
		extras := &gotifyExtras{}
		if notif.DeepLink != "" {
			extras.ClientDisplay = &gotifyClientDisplay{ContentType: "text/plain"}
			extras.ClientNotification = &gotifyClientNotification{Click: &gotifyClick{URL: notif.DeepLink}}
		}
		if notif.ImageURL != "" {
			if extras.ClientNotification == nil {
				extras.ClientNotification = &gotifyClientNotification{}
			}
			extras.ClientNotification.BigImageURL = notif.ImageURL
		}
		payload.Extras = extras
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("gotify: encode payload: %w", err)
	}

	reqURL := server + "/message"

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("gotify: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", token)

	client := g.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gotify: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxBody = 512
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("gotify: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}
