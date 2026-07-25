package backend

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

func TestPushoverParseTargetMissingToken(t *testing.T) {
	p := &pushover{}

	if _, err := p.ParseTarget(map[string]any{"pushover_user": "u1"}); err == nil {
		t.Fatalf("ParseTarget with no token: got nil error, want error")
	}
	if _, err := p.ParseTarget(map[string]any{"pushover_token": "", "pushover_user": "u1"}); err == nil {
		t.Fatalf("ParseTarget with empty token: got nil error, want error")
	}
}

func TestPushoverParseTargetMissingUser(t *testing.T) {
	p := &pushover{}

	if _, err := p.ParseTarget(map[string]any{"pushover_token": "tk"}); err == nil {
		t.Fatalf("ParseTarget with no user: got nil error, want error")
	}
	if _, err := p.ParseTarget(map[string]any{"pushover_token": "tk", "pushover_user": ""}); err == nil {
		t.Fatalf("ParseTarget with empty user: got nil error, want error")
	}
}

func TestPushoverParseTargetOK(t *testing.T) {
	p := &pushover{}

	cfg, err := p.ParseTarget(map[string]any{"pushover_token": "tk_secret", "pushover_user": "u1"})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["token"] != "tk_secret" {
		t.Errorf("token = %q, want %q", cfg["token"], "tk_secret")
	}
	if cfg["user"] != "u1" {
		t.Errorf("user = %q, want %q", cfg["user"], "u1")
	}
}

func TestPushoverSendBasic(t *testing.T) {
	var gotMethod, gotPath string
	var gotValues url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotValues, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newPushover()
	p.client = srv.Client()
	p.baseURL = srv.URL + "/1/messages.json"

	cfg := map[string]string{"token": "tk_secret", "user": "u1"}
	notif := sdk.Notification{Title: "Motion detected", Body: "Front door camera", Severity: sdk.SeverityInfo}

	if err := p.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/1/messages.json" {
		t.Errorf("path = %q, want /1/messages.json", gotPath)
	}
	if gotValues.Get("token") != "tk_secret" {
		t.Errorf("token = %q, want %q", gotValues.Get("token"), "tk_secret")
	}
	if gotValues.Get("user") != "u1" {
		t.Errorf("user = %q, want %q", gotValues.Get("user"), "u1")
	}
	if gotValues.Get("title") != "Motion detected" {
		t.Errorf("title = %q, want %q", gotValues.Get("title"), "Motion detected")
	}
	if gotValues.Get("message") != "Front door camera" {
		t.Errorf("message = %q, want %q", gotValues.Get("message"), "Front door camera")
	}
	if gotValues.Get("priority") != "0" {
		t.Errorf("priority = %q, want %q", gotValues.Get("priority"), "0")
	}
	if gotValues.Get("url") != "" {
		t.Errorf("url = %q, want empty when DeepLink unset", gotValues.Get("url"))
	}
}

func TestPushoverSendMessageFallsBackToTitle(t *testing.T) {
	var gotValues url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotValues, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newPushover()
	p.client = srv.Client()
	p.baseURL = srv.URL

	cfg := map[string]string{"token": "tk", "user": "u1"}
	notif := sdk.Notification{Title: "Motion detected"}

	if err := p.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if gotValues.Get("message") != "Motion detected" {
		t.Errorf("message = %q, want fallback to title %q", gotValues.Get("message"), "Motion detected")
	}
}

func TestPushoverSendPriorityBySeverity(t *testing.T) {
	priorityFor := func(sev sdk.Severity) string {
		var gotValues url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotValues, _ = url.ParseQuery(string(b))
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		p := newPushover()
		p.client = srv.Client()
		p.baseURL = srv.URL

		cfg := map[string]string{"token": "tk", "user": "u1"}
		_ = p.Send(nil, cfg, sdk.Notification{Title: "x", Severity: sev})
		return gotValues.Get("priority")
	}

	if got := priorityFor(sdk.SeverityInfo); got != "0" {
		t.Errorf("Info priority = %q, want %q", got, "0")
	}
	if got := priorityFor(sdk.SeverityCritical); got != "1" {
		t.Errorf("Critical priority = %q, want %q", got, "1")
	}
	if got := priorityFor(sdk.SeverityWarn); got != "1" {
		t.Errorf("Warn priority = %q, want %q", got, "1")
	}
	if got := priorityFor(sdk.SeverityError); got != "1" {
		t.Errorf("Error priority = %q, want %q", got, "1")
	}
}

func TestPushoverSendLinkOnlyWhenDeepLinkAbsolute(t *testing.T) {
	valuesFor := func(deepLink string) url.Values {
		var gotValues url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotValues, _ = url.ParseQuery(string(b))
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		p := newPushover()
		p.client = srv.Client()
		p.baseURL = srv.URL

		cfg := map[string]string{"token": "tk", "user": "u1"}
		_ = p.Send(nil, cfg, sdk.Notification{Title: "x", DeepLink: deepLink})
		return gotValues
	}

	abs := valuesFor("https://camera.example.com/cameras/cam-1")
	if abs.Get("url") != "https://camera.example.com/cameras/cam-1" {
		t.Errorf("url = %q, want the absolute deep link", abs.Get("url"))
	}
	if abs.Get("url_title") != "Open camera" {
		t.Errorf("url_title = %q, want %q", abs.Get("url_title"), "Open camera")
	}

	rel := valuesFor("/cameras/cam-1")
	if rel.Get("url") != "" {
		t.Errorf("url = %q, want empty for a relative DeepLink", rel.Get("url"))
	}
	if rel.Get("url_title") != "" {
		t.Errorf("url_title = %q, want empty for a relative DeepLink", rel.Get("url_title"))
	}
}

func TestPushoverSendWithThumbnailUsesMultipartAttachment(t *testing.T) {
	var gotContentType string
	var gotFields url.Values
	var gotFileBytes []byte
	var gotFileContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, params, err := mime.ParseMediaType(gotContentType)
		if err != nil {
			t.Fatalf("parse content-type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		gotFields = url.Values{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "attachment" {
				gotFileBytes = b
				gotFileContentType = part.Header.Get("Content-Type")
			} else {
				gotFields.Set(part.FormName(), string(b))
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newPushover()
	p.client = srv.Client()
	p.baseURL = srv.URL

	thumb := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	cfg := map[string]string{"token": "tk", "user": "u1"}
	notif := sdk.Notification{Title: "Motion detected", Body: "Front door", Thumbnail: thumb}

	if err := p.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", gotContentType)
	}
	if gotFields.Get("message") != "Front door" {
		t.Errorf("message = %q, want %q", gotFields.Get("message"), "Front door")
	}
	if !bytes.Equal(gotFileBytes, thumb) {
		t.Errorf("attachment bytes = %v, want %v", gotFileBytes, thumb)
	}
	if gotFileContentType != "image/jpeg" {
		t.Errorf("attachment Content-Type = %q, want %q", gotFileContentType, "image/jpeg")
	}
}

func TestPushoverSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	p := newPushover()
	p.client = srv.Client()
	p.baseURL = srv.URL

	cfg := map[string]string{"token": "tk", "user": "u1"}
	notif := sdk.Notification{Title: "x"}

	err := p.Send(nil, cfg, notif)
	if err == nil {
		t.Fatalf("Send: got nil error, want error on non-2xx response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
}

func TestPushoverIDAndLabel(t *testing.T) {
	p := &pushover{}
	if p.ID() != "pushover" {
		t.Errorf("ID() = %q, want %q", p.ID(), "pushover")
	}
	if p.Label() != "Pushover" {
		t.Errorf("Label() = %q, want %q", p.Label(), "Pushover")
	}
}

func TestPushoverSchemaConditions(t *testing.T) {
	p := &pushover{}
	schema := p.Schema()

	byKey := map[string]sdk.JsonSchema{}
	for _, f := range schema {
		byKey[f.Key] = f
		if len(f.Condition) != 1 || f.Condition[0].Key != "service" || f.Condition[0].Value != "pushover" {
			t.Errorf("field %q: Condition = %+v, want gated on service==pushover", f.Key, f.Condition)
		}
	}

	token, ok := byKey["pushover_token"]
	if !ok {
		t.Fatalf("schema missing %q field", "pushover_token")
	}
	if !token.Required || token.Format != sdk.StringFormatPassword {
		t.Errorf("token = %+v, want Required=true Format=password", token)
	}

	user, ok := byKey["pushover_user"]
	if !ok {
		t.Fatalf("schema missing %q field", "pushover_user")
	}
	if !user.Required || user.Format != sdk.StringFormatPassword {
		t.Errorf("user = %+v, want Required=true Format=password", user)
	}
}
