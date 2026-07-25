package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

func TestWebhookParseTargetMissingURL(t *testing.T) {
	w := &webhook{}

	if _, err := w.ParseTarget(map[string]any{}); err == nil {
		t.Fatalf("ParseTarget with no url: got nil error, want error")
	}
	if _, err := w.ParseTarget(map[string]any{"webhook_url": ""}); err == nil {
		t.Fatalf("ParseTarget with empty url: got nil error, want error")
	}
}

func TestWebhookParseTargetDefaultsMethodToPost(t *testing.T) {
	w := &webhook{}

	cfg, err := w.ParseTarget(map[string]any{"webhook_url": "https://example.com/hook"})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["method"] != http.MethodPost {
		t.Errorf("method = %q, want %q", cfg["method"], http.MethodPost)
	}
	if cfg["url"] != "https://example.com/hook" {
		t.Errorf("url = %q, want %q", cfg["url"], "https://example.com/hook")
	}
}

func TestWebhookParseTargetHonorsMethodOverride(t *testing.T) {
	w := &webhook{}

	cfg, err := w.ParseTarget(map[string]any{"webhook_url": "https://example.com/hook", "webhook_method": "PUT"})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["method"] != http.MethodPut {
		t.Errorf("method = %q, want %q", cfg["method"], http.MethodPut)
	}
}

func TestWebhookParseTargetHeaderNameRequiresHeaderValue(t *testing.T) {
	w := &webhook{}

	if _, err := w.ParseTarget(map[string]any{
		"webhook_url":        "https://example.com/hook",
		"webhook_headerName": "X-Api-Key",
	}); err == nil {
		t.Fatalf("ParseTarget with headerName but no headerValue: got nil error, want error")
	}
}

func TestWebhookParseTargetHeaderValueRequiresHeaderName(t *testing.T) {
	w := &webhook{}

	if _, err := w.ParseTarget(map[string]any{
		"webhook_url":         "https://example.com/hook",
		"webhook_headerValue": "secret",
	}); err == nil {
		t.Fatalf("ParseTarget with headerValue but no headerName: got nil error, want error")
	}
}

func TestWebhookParseTargetHeaderPairAccepted(t *testing.T) {
	w := &webhook{}

	cfg, err := w.ParseTarget(map[string]any{
		"webhook_url":         "https://example.com/hook",
		"webhook_headerName":  "X-Api-Key",
		"webhook_headerValue": "secret",
	})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["headerName"] != "X-Api-Key" {
		t.Errorf("headerName = %q, want %q", cfg["headerName"], "X-Api-Key")
	}
	if cfg["headerValue"] != "secret" {
		t.Errorf("headerValue = %q, want %q", cfg["headerValue"], "secret")
	}
}

// decodedWebhookPayload is a loose structural mirror of what webhook.Send is
// expected to emit, used only to assert on the JSON body in tests.
type decodedWebhookPayload struct {
	Title     string            `json:"title"`
	Subtitle  string            `json:"subtitle"`
	Body      string            `json:"body"`
	Severity  string            `json:"severity"`
	Tag       string            `json:"tag"`
	ImageURL  string            `json:"imageUrl"`
	DeepLink  string            `json:"deepLink"`
	Data      map[string]string `json:"data"`
	CreatedAt int64             `json:"createdAt"`
}

func fixedClock(ts time.Time) func() time.Time {
	return func() time.Time { return ts }
}

func TestWebhookSendDefaultsToPostWithJSONContentType(t *testing.T) {
	var gotMethod, gotContentType string
	var gotBody decodedWebhookPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := newWebhook()
	w.client = srv.Client()
	fixedTS := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	w.now = fixedClock(fixedTS)

	cfg := map[string]string{"url": srv.URL, "method": http.MethodPost}
	notif := sdk.Notification{
		Title:    "Motion detected",
		Subtitle: "Front door",
		Body:     "Someone is at the door",
		Severity: sdk.SeverityWarn,
		Tag:      "motion:cam-1",
		ImageURL: "https://example.com/snap.jpg",
		DeepLink: "/cameras/cam-1",
		Data:     map[string]string{"cameraId": "cam-1"},
	}

	if err := w.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody.Title != "Motion detected" {
		t.Errorf("title = %q, want %q", gotBody.Title, "Motion detected")
	}
	if gotBody.Subtitle != "Front door" {
		t.Errorf("subtitle = %q, want %q", gotBody.Subtitle, "Front door")
	}
	if gotBody.Body != "Someone is at the door" {
		t.Errorf("body = %q, want %q", gotBody.Body, "Someone is at the door")
	}
	if gotBody.Severity != string(sdk.SeverityWarn) {
		t.Errorf("severity = %q, want %q", gotBody.Severity, sdk.SeverityWarn)
	}
	if gotBody.Tag != "motion:cam-1" {
		t.Errorf("tag = %q, want %q", gotBody.Tag, "motion:cam-1")
	}
	if gotBody.ImageURL != "https://example.com/snap.jpg" {
		t.Errorf("imageUrl = %q, want %q", gotBody.ImageURL, "https://example.com/snap.jpg")
	}
	if gotBody.DeepLink != "/cameras/cam-1" {
		t.Errorf("deepLink = %q, want %q", gotBody.DeepLink, "/cameras/cam-1")
	}
	if gotBody.Data["cameraId"] != "cam-1" {
		t.Errorf("data[cameraId] = %q, want %q", gotBody.Data["cameraId"], "cam-1")
	}
	if want := fixedTS.UnixMilli(); gotBody.CreatedAt != want {
		t.Errorf("createdAt = %d, want %d", gotBody.CreatedAt, want)
	}
}

func TestWebhookSendHonorsMethodOverride(t *testing.T) {
	var gotMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := newWebhook()
	w.client = srv.Client()
	w.now = fixedClock(time.Unix(0, 0))

	cfg := map[string]string{"url": srv.URL, "method": http.MethodPut}
	notif := sdk.Notification{Title: "x"}

	if err := w.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
}

func TestWebhookSendCustomHeaderPresentWhenConfigured(t *testing.T) {
	var gotHeaderValue string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaderValue = r.Header.Get("X-Api-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := newWebhook()
	w.client = srv.Client()
	w.now = fixedClock(time.Unix(0, 0))

	cfg := map[string]string{
		"url":         srv.URL,
		"method":      http.MethodPost,
		"headerName":  "X-Api-Key",
		"headerValue": "secret",
	}
	notif := sdk.Notification{Title: "x"}

	if err := w.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if gotHeaderValue != "secret" {
		t.Errorf("X-Api-Key header = %q, want %q", gotHeaderValue, "secret")
	}
}

func TestWebhookSendCustomHeaderAbsentWhenNotConfigured(t *testing.T) {
	var sawHeader bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			sawHeader = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := newWebhook()
	w.client = srv.Client()
	w.now = fixedClock(time.Unix(0, 0))

	cfg := map[string]string{"url": srv.URL, "method": http.MethodPost}
	notif := sdk.Notification{Title: "x"}

	if err := w.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if sawHeader {
		t.Errorf("X-Api-Key header present, want absent when not configured")
	}
}

func TestWebhookSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	w := newWebhook()
	w.client = srv.Client()
	w.now = fixedClock(time.Unix(0, 0))

	cfg := map[string]string{"url": srv.URL, "method": http.MethodPost}
	notif := sdk.Notification{Title: "x"}

	err := w.Send(nil, cfg, notif)
	if err == nil {
		t.Fatalf("Send: got nil error, want error on non-2xx response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
}

func TestWebhookIDAndLabel(t *testing.T) {
	w := &webhook{}
	if w.ID() != "webhook" {
		t.Errorf("ID() = %q, want %q", w.ID(), "webhook")
	}
	if w.Label() != "Generic webhook" {
		t.Errorf("Label() = %q, want %q", w.Label(), "Generic webhook")
	}
}

func TestWebhookSchemaConditions(t *testing.T) {
	w := &webhook{}
	schema := w.Schema()

	byKey := map[string]sdk.JsonSchema{}
	for _, f := range schema {
		byKey[f.Key] = f
		if len(f.Condition) != 1 || f.Condition[0].Key != "service" || f.Condition[0].Value != "webhook" {
			t.Errorf("field %q: Condition = %+v, want gated on service==webhook", f.Key, f.Condition)
		}
	}

	url, ok := byKey["webhook_url"]
	if !ok {
		t.Fatalf("schema missing %q field", "webhook_url")
	}
	if !url.Required {
		t.Errorf("url Required = false, want true")
	}

	method, ok := byKey["webhook_method"]
	if !ok {
		t.Fatalf("schema missing %q field", "webhook_method")
	}
	if len(method.Enum) != 2 || method.Enum[0] != "POST" || method.Enum[1] != "PUT" {
		t.Errorf("method Enum = %+v, want [POST PUT]", method.Enum)
	}

	headerValue, ok := byKey["webhook_headerValue"]
	if !ok {
		t.Fatalf("schema missing %q field", "webhook_headerValue")
	}
	if headerValue.Format != sdk.StringFormatPassword {
		t.Errorf("headerValue Format = %q, want %q", headerValue.Format, sdk.StringFormatPassword)
	}
}
