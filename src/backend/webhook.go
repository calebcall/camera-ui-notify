package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// webhook implements Backend as a generic HTTP webhook. Delivery is a single
// HTTP request (POST or PUT) carrying a JSON body describing the
// notification, with an optional custom header for simple shared-secret
// authentication.
type webhook struct {
	// client performs the HTTP request. Defaulted by newWebhook; overridable
	// in tests so Send never touches the real network.
	client *http.Client
	// now returns the current time, used to stamp createdAt. Defaulted by
	// newWebhook to time.Now; overridable in tests for a deterministic value.
	now func() time.Time
}

// newWebhook constructs a webhook backend with a client suitable for
// production use (~10s timeout) and a real clock. Tests override the client
// and now fields to point at an httptest server and a fixed time.
func newWebhook() *webhook {
	return &webhook{
		client: &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
}

func init() {
	Register(newWebhook())
}

func (w *webhook) ID() string    { return "webhook" }
func (w *webhook) Label() string { return "Generic webhook" }

func (w *webhook) Schema() []sdk.JsonSchema {
	cond := []sdk.SchemaCondition{{Key: "service", Value: "webhook"}}
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "webhook_url",
			Title:       "URL",
			Description: "Endpoint that receives the notification.",
			Required:    true,
			Condition:   cond,
		},
		{
			Type:         sdk.JsonSchemaTypeString,
			Key:          "webhook_method",
			Title:        "Method",
			Description:  "HTTP method used to deliver the notification.",
			Enum:         []string{http.MethodPost, http.MethodPut},
			DefaultValue: http.MethodPost,
			Condition:    cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "webhook_headerName",
			Title:       "Header name",
			Description: "Optional custom header sent with every request (e.g. for a shared secret).",
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "webhook_headerValue",
			Title:       "Header value",
			Description: "Value of the custom header. Required if header name is set.",
			Format:      sdk.StringFormatPassword,
			Condition:   cond,
		},
	}
}

// ParseTarget validates the raw registration input and returns the
// normalized config persisted in NotifierDevice.Metadata. Schema field keys
// are namespaced (webhook_*) so they don't collide with other backends'
// fields in the flattened NotificationSettings() form; the returned cfg
// uses the short keys Send reads.
func (w *webhook) ParseTarget(input map[string]any) (map[string]string, error) {
	rawURL, _ := input["webhook_url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, errors.New("webhook: url is required")
	}

	method, _ := input["webhook_method"].(string)
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodPost && method != http.MethodPut {
		return nil, fmt.Errorf("webhook: method must be POST or PUT, got %q", method)
	}

	headerName, _ := input["webhook_headerName"].(string)
	headerName = strings.TrimSpace(headerName)
	headerValue, _ := input["webhook_headerValue"].(string)
	headerValue = strings.TrimSpace(headerValue)

	if headerName != "" && headerValue == "" {
		return nil, errors.New("webhook: headerValue is required when headerName is set")
	}
	if headerValue != "" && headerName == "" {
		return nil, errors.New("webhook: headerName is required when headerValue is set")
	}

	cfg := map[string]string{
		"url":    rawURL,
		"method": method,
	}
	if headerName != "" {
		cfg["headerName"] = headerName
		cfg["headerValue"] = headerValue
	}

	return cfg, nil
}

// webhookPayload is the JSON body posted/put to the configured webhook URL.
type webhookPayload struct {
	Title           string            `json:"title"`
	Subtitle        string            `json:"subtitle,omitempty"`
	Body            string            `json:"body,omitempty"`
	Severity        string            `json:"severity,omitempty"`
	Tag             string            `json:"tag,omitempty"`
	ImageURL        string            `json:"imageUrl,omitempty"`
	DeepLink        string            `json:"deepLink,omitempty"`
	Data            map[string]string `json:"data,omitempty"`
	CreatedAt       int64             `json:"createdAt"`
	ThumbnailBase64 string            `json:"thumbnailBase64,omitempty"`
}

// Send delivers a single notification as a JSON HTTP request.
func (w *webhook) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	targetURL := cfg["url"]
	method := cfg["method"]
	if method == "" {
		method = http.MethodPost
	}

	now := w.now
	if now == nil {
		now = time.Now
	}

	payload := webhookPayload{
		Title:     notif.Title,
		Subtitle:  notif.Subtitle,
		Body:      notif.Body,
		Severity:  string(notif.Severity),
		Tag:       notif.Tag,
		ImageURL:  notif.ImageURL,
		DeepLink:  notif.DeepLink,
		Data:      notif.Data,
		CreatedAt: now().UnixMilli(),
	}
	if len(notif.Thumbnail) > 0 {
		payload.ThumbnailBase64 = base64.StdEncoding.EncodeToString(notif.Thumbnail)
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook: encode payload: %w", err)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if headerName := cfg["headerName"]; headerName != "" {
		req.Header.Set(headerName, cfg["headerValue"])
	}

	client := w.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxBody = 512
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("webhook: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}
