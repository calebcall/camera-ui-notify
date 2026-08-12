package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// defaultNtfyServer is used when the user leaves the server field blank at
// registration time.
const defaultNtfyServer = "https://ntfy.sh"

// ntfy implements Backend for https://ntfy.sh (or a self-hosted ntfy
// server). Delivery is a single unauthenticated-or-bearer-token HTTP POST
// per https://docs.ntfy.sh/publish/.
type ntfy struct {
	// client performs the HTTP request. Defaulted by newNtfy; overridable in
	// tests so Send never touches the real network.
	client *http.Client
}

// newNtfy constructs an ntfy backend with a client suitable for production
// use (~10s timeout). Tests override the client field to point at an
// httptest server.
func newNtfy() *ntfy {
	return &ntfy{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func init() {
	Register(newNtfy())
}

func (n *ntfy) ID() string    { return "ntfy" }
func (n *ntfy) Label() string { return "ntfy" }

func (n *ntfy) Schema() []sdk.JsonSchema {
	cond := []sdk.SchemaCondition{{Key: "service", Value: "ntfy"}}
	return []sdk.JsonSchema{
		{
			Type:         sdk.JsonSchemaTypeString,
			Key:          "ntfy_server",
			Title:        "Server",
			Description:  "Base URL of the ntfy server.",
			DefaultValue: defaultNtfyServer,
			Condition:    cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "ntfy_topic",
			Title:       "Topic",
			Description: "The ntfy topic to publish to.",
			Required:    true,
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "ntfy_token",
			Title:       "Access Token",
			Description: "Optional access token for a protected topic.",
			Format:      sdk.StringFormatPassword,
			Condition:   cond,
		},
	}
}

// ParseTarget validates the raw registration input and returns the
// normalized config persisted in NotifierDevice.Metadata. Schema field keys
// are namespaced (ntfy_*) so they don't collide with other backends' fields
// in the flattened NotificationSettings() form; the returned cfg uses the
// short keys Send reads.
func (n *ntfy) ParseTarget(input map[string]any) (map[string]string, error) {
	topic, _ := input["ntfy_topic"].(string)
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil, errors.New("ntfy: topic is required")
	}

	server, _ := input["ntfy_server"].(string)
	server = strings.TrimSpace(server)
	if server == "" {
		server = defaultNtfyServer
	}
	server = strings.TrimRight(server, "/")

	cfg := map[string]string{
		"server": server,
		"topic":  topic,
	}

	if token, _ := input["ntfy_token"].(string); token != "" {
		cfg["token"] = token
	}

	return cfg, nil
}

// ntfyAction is one entry of the JSON array published in ntfy's Actions
// header (https://docs.ntfy.sh/publish/#action-buttons).
type ntfyAction struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
}

// ntfyVideoActions renders the Actions header value carrying a "view" button
// for notif's clip, or "" when there is no usable clip URL.
//
// ntfy accepts either a comma-separated shorthand or a JSON array here; the
// JSON form is used because a clip URL is free to contain the commas and
// semicolons the shorthand treats as separators. json.Marshal escapes any
// control character it encounters, so the result is always a single header
// line.
func ntfyVideoActions(notif sdk.Notification) string {
	link := VideoLink(notif)
	if link == "" {
		return ""
	}
	buf, err := json.Marshal([]ntfyAction{{Action: "view", Label: VideoLinkLabel, URL: link}})
	if err != nil {
		return ""
	}
	return string(buf)
}

// Send delivers a single notification via an ntfy publish request. When an
// inline Thumbnail is present, it is published as a file attachment (the
// request body is the raw JPEG bytes) per
// https://docs.ntfy.sh/publish/#attach-local-file — ntfy then requires the
// notification text to travel in the Message header rather than the body,
// since the body is the file. Otherwise the notification text is the
// request body, as before, and ImageURL (if any) is passed through as a
// remote Attach/Icon URL.
func (n *ntfy) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	server := cfg["server"]
	topic := cfg["topic"]

	body := notif.Body
	if body == "" {
		body = notif.Title
	}

	url := server + "/" + topic

	if ctx == nil {
		ctx = context.Background()
	}

	var req *http.Request
	var err error
	if len(notif.Thumbnail) > 0 {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(notif.Thumbnail))
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	}
	if err != nil {
		return fmt.Errorf("ntfy: build request: %w", err)
	}

	req.Header.Set("Title", notif.Title)
	// ntfy has no way to replace an already-published message, so a silent
	// update-only publish (the AI description superseding its detection
	// alert) is delivered at min priority instead: it still lands in the
	// topic, but ntfy clients deliver priority 1 without sound or vibration.
	priority := PriorityScale(notif.Severity, 1, 5)
	if SilentDelivery(notif) {
		priority = 1
	}
	req.Header.Set("Priority", strconv.Itoa(priority))
	if notif.DeepLink != "" {
		req.Header.Set("Click", notif.DeepLink)
	}
	if len(notif.Thumbnail) > 0 {
		req.Header.Set("Filename", "snapshot.jpg")
		req.Header.Set("Message", body)
	} else if notif.ImageURL != "" {
		req.Header.Set("Attach", notif.ImageURL)
		req.Header.Set("Icon", notif.ImageURL)
	}
	// The clip goes on an action button rather than into Attach: ntfy allows
	// exactly one attachment per message, and taking it for the video would
	// cost the snapshot — which is the part every ntfy client renders inline.
	// A "view" action opens the URL in the phone's browser/player instead,
	// and sits alongside the snapshot rather than replacing it.
	if actions := ntfyVideoActions(notif); actions != "" {
		req.Header.Set("Actions", actions)
	}
	if token := cfg["token"]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := n.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxBody = 512
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("ntfy: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}
