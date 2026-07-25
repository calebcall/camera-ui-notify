package backend

import (
	"bytes"
	"context"
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

// defaultPushoverBaseURL is the production Pushover Messages API endpoint,
// used when baseURL is left at its zero value.
const defaultPushoverBaseURL = "https://api.pushover.net/1/messages.json"

// pushover implements Backend for https://pushover.net. Delivery is a
// single HTTP POST to the Messages API
// (https://pushover.net/api) — application/x-www-form-urlencoded when
// there is no image, multipart/form-data with an "attachment" file part
// when an inline Thumbnail is present.
type pushover struct {
	// client performs the HTTP request. Defaulted by newPushover; overridable
	// in tests so Send never touches the real network.
	client *http.Client
	// baseURL is the Messages API endpoint. Defaulted by newPushover;
	// overridable in tests to point at an httptest server.
	baseURL string
}

// newPushover constructs a Pushover backend with a client suitable for
// production use (~10s timeout) and the real API endpoint. Tests override
// the client and baseURL fields to point at an httptest server.
func newPushover() *pushover {
	return &pushover{
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: defaultPushoverBaseURL,
	}
}

func init() {
	Register(newPushover())
}

func (p *pushover) ID() string    { return "pushover" }
func (p *pushover) Label() string { return "Pushover" }

func (p *pushover) Schema() []sdk.JsonSchema {
	cond := []sdk.SchemaCondition{{Key: "service", Value: "pushover"}}
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "pushover_token",
			Title:       "API Token/Key",
			Description: "The Pushover application API token.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "pushover_user",
			Title:       "User or Group Key",
			Description: "The Pushover user or group key to deliver to.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   cond,
		},
	}
}

// ParseTarget validates the raw registration input and returns the
// normalized config persisted in NotifierDevice.Metadata. Schema field keys
// are namespaced (pushover_*) so they don't collide with other backends'
// fields in the flattened NotificationSettings() form; the returned cfg
// uses the short keys Send reads.
func (p *pushover) ParseTarget(input map[string]any) (map[string]string, error) {
	token, _ := input["pushover_token"].(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("pushover: token is required")
	}

	user, _ := input["pushover_user"].(string)
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, errors.New("pushover: user is required")
	}

	return map[string]string{
		"token": token,
		"user":  user,
	}, nil
}

// pushoverPriority maps notif.Severity onto Pushover's priority parameter.
// Pushover's emergency priority (2) requires retry/expire parameters and
// repeats delivery until acknowledged, which is wrong for detection
// notifications, so this never returns 2: SeverityInfo (and unknown/empty)
// maps to 0 (normal), everything else maps to 1 (high).
func pushoverPriority(sev sdk.Severity) int {
	if sev == sdk.SeverityInfo || sev == "" {
		return 0
	}
	return 1
}

// Send delivers a single notification via the Pushover Messages API.
func (p *pushover) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	message := notif.Body
	if message == "" {
		message = notif.Title
	}

	fields := map[string]string{
		"token":    cfg["token"],
		"user":     cfg["user"],
		"title":    notif.Title,
		"message":  message,
		"priority": strconv.Itoa(pushoverPriority(notif.Severity)),
	}
	if strings.HasPrefix(notif.DeepLink, "http") {
		fields["url"] = notif.DeepLink
		fields["url_title"] = "Open camera"
	}

	baseURL := p.baseURL
	if baseURL == "" {
		baseURL = defaultPushoverBaseURL
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var req *http.Request
	var err error
	if len(notif.Thumbnail) > 0 {
		req, err = newPushoverAttachmentRequest(ctx, baseURL, fields, notif.Thumbnail)
	} else {
		values := url.Values{}
		for k, v := range fields {
			values.Set(k, v)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, baseURL, strings.NewReader(values.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return fmt.Errorf("pushover: build request: %w", err)
	}

	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("pushover: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxBody = 512
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("pushover: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}

// newPushoverAttachmentRequest builds a multipart/form-data POST carrying
// the given text fields plus the thumbnail bytes as an "attachment" file
// part (filename snapshot.jpg, Content-Type image/jpeg), per
// https://pushover.net/api#attachments.
func newPushoverAttachmentRequest(ctx context.Context, targetURL string, fields map[string]string, thumbnail []byte) (*http.Request, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, err
		}
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="attachment"; filename="snapshot.jpg"`)
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
