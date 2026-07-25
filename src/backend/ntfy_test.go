package backend

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

func TestNtfyParseTargetMissingTopic(t *testing.T) {
	n := &ntfy{}

	if _, err := n.ParseTarget(map[string]any{}); err == nil {
		t.Fatalf("ParseTarget with no topic: got nil error, want error")
	}
	if _, err := n.ParseTarget(map[string]any{"ntfy_topic": ""}); err == nil {
		t.Fatalf("ParseTarget with empty topic: got nil error, want error")
	}
}

func TestNtfyParseTargetServerDefault(t *testing.T) {
	n := &ntfy{}

	cfg, err := n.ParseTarget(map[string]any{"ntfy_topic": "alerts"})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["server"] != "https://ntfy.sh" {
		t.Errorf("server = %q, want default %q", cfg["server"], "https://ntfy.sh")
	}
	if cfg["topic"] != "alerts" {
		t.Errorf("topic = %q, want %q", cfg["topic"], "alerts")
	}
	if _, ok := cfg["token"]; ok {
		t.Errorf("token present in cfg, want absent when not supplied")
	}
}

func TestNtfyParseTargetTrimsTrailingSlash(t *testing.T) {
	n := &ntfy{}

	cfg, err := n.ParseTarget(map[string]any{
		"ntfy_server": "https://ntfy.example.com/",
		"ntfy_topic":  "alerts",
	})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["server"] != "https://ntfy.example.com" {
		t.Errorf("server = %q, want trailing slash trimmed", cfg["server"])
	}
}

func TestNtfyParseTargetTokenPassthrough(t *testing.T) {
	n := &ntfy{}

	cfg, err := n.ParseTarget(map[string]any{
		"ntfy_topic": "alerts",
		"ntfy_token": "tk_secret",
	})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["token"] != "tk_secret" {
		t.Errorf("token = %q, want %q", cfg["token"], "tk_secret")
	}
}

func TestNtfySendBasic(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "topic": "alerts"}
	notif := sdk.Notification{Title: "Motion detected", Body: "Front door camera", Severity: sdk.SeverityInfo}

	if err := n.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/alerts" {
		t.Errorf("path = %q, want /alerts", gotPath)
	}
	if gotHeaders.Get("Title") != "Motion detected" {
		t.Errorf("Title header = %q, want %q", gotHeaders.Get("Title"), "Motion detected")
	}
	if gotHeaders.Get("Priority") != strconv.Itoa(PriorityScale(sdk.SeverityInfo, 1, 5)) {
		t.Errorf("Priority header = %q, want %d", gotHeaders.Get("Priority"), PriorityScale(sdk.SeverityInfo, 1, 5))
	}
	if gotBody != "Front door camera" {
		t.Errorf("body = %q, want %q", gotBody, "Front door camera")
	}
	if gotHeaders.Get("Click") != "" {
		t.Errorf("Click header = %q, want empty when DeepLink unset", gotHeaders.Get("Click"))
	}
	if gotHeaders.Get("Attach") != "" {
		t.Errorf("Attach header = %q, want empty when ImageURL unset", gotHeaders.Get("Attach"))
	}
	if gotHeaders.Get("Icon") != "" {
		t.Errorf("Icon header = %q, want empty when ImageURL unset", gotHeaders.Get("Icon"))
	}
	if gotHeaders.Get("Authorization") != "" {
		t.Errorf("Authorization header = %q, want empty when token unset", gotHeaders.Get("Authorization"))
	}
}

func TestNtfySendBodyFallsBackToTitle(t *testing.T) {
	var gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "topic": "alerts"}
	notif := sdk.Notification{Title: "Motion detected"}

	if err := n.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if gotBody != "Motion detected" {
		t.Errorf("body = %q, want fallback to title %q", gotBody, "Motion detected")
	}
}

func TestNtfySendPriorityBySeverity(t *testing.T) {
	cases := []struct {
		sev  sdk.Severity
		want int
	}{
		{sdk.SeverityInfo, 1},
		{sdk.SeverityCritical, 5},
	}

	for _, tc := range cases {
		var gotPriority string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPriority = r.Header.Get("Priority")
			w.WriteHeader(http.StatusOK)
		}))

		n := newNtfy()
		n.client = srv.Client()
		cfg := map[string]string{"server": srv.URL, "topic": "alerts"}
		notif := sdk.Notification{Title: "x", Severity: tc.sev}

		if err := n.Send(nil, cfg, notif); err != nil {
			t.Fatalf("Send: unexpected error: %v", err)
		}
		if gotPriority != strconv.Itoa(tc.want) {
			t.Errorf("severity %q: Priority header = %q, want %d", tc.sev, gotPriority, tc.want)
		}
		srv.Close()
	}

	// Warn and Error should fall strictly between Info and Critical.
	priorityFor := func(sev sdk.Severity) int {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("Priority")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		n := newNtfy()
		n.client = srv.Client()
		cfg := map[string]string{"server": srv.URL, "topic": "alerts"}
		_ = n.Send(nil, cfg, sdk.Notification{Title: "x", Severity: sev})
		v, _ := strconv.Atoi(got)
		return v
	}

	info := priorityFor(sdk.SeverityInfo)
	warn := priorityFor(sdk.SeverityWarn)
	errP := priorityFor(sdk.SeverityError)
	crit := priorityFor(sdk.SeverityCritical)
	if !(info <= warn && warn <= errP && errP <= crit) {
		t.Fatalf("priorities not monotonic: info=%d warn=%d error=%d critical=%d", info, warn, errP, crit)
	}
}

func TestNtfySendOptionalHeaders(t *testing.T) {
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "topic": "alerts", "token": "tk_secret"}
	notif := sdk.Notification{
		Title:    "Motion detected",
		Body:     "Front door",
		Severity: sdk.SeverityWarn,
		DeepLink: "/cameras/cam-1",
		ImageURL: "https://example.com/snap.jpg",
	}

	if err := n.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotHeaders.Get("Click") != "/cameras/cam-1" {
		t.Errorf("Click header = %q, want %q", gotHeaders.Get("Click"), "/cameras/cam-1")
	}
	if gotHeaders.Get("Attach") != "https://example.com/snap.jpg" {
		t.Errorf("Attach header = %q, want %q", gotHeaders.Get("Attach"), "https://example.com/snap.jpg")
	}
	if gotHeaders.Get("Icon") != "https://example.com/snap.jpg" {
		t.Errorf("Icon header = %q, want %q", gotHeaders.Get("Icon"), "https://example.com/snap.jpg")
	}
	if gotHeaders.Get("Authorization") != "Bearer tk_secret" {
		t.Errorf("Authorization header = %q, want %q", gotHeaders.Get("Authorization"), "Bearer tk_secret")
	}
}

func TestNtfySendWithThumbnailPublishesFileAttachment(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()

	thumb := []byte{0xFF, 0xD8, 0xFF, 0xD9} // minimal JPEG-ish bytes
	cfg := map[string]string{"server": srv.URL, "topic": "alerts"}
	notif := sdk.Notification{
		Title:     "Motion detected",
		Body:      "Front door camera",
		Severity:  sdk.SeverityWarn,
		Thumbnail: thumb,
		DeepLink:  "https://camera.example.com/cameras/cam-1",
	}

	if err := n.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if !bytes.Equal(gotBody, thumb) {
		t.Errorf("request body = %v, want the raw thumbnail bytes %v", gotBody, thumb)
	}
	if gotHeaders.Get("Filename") != "snapshot.jpg" {
		t.Errorf("Filename header = %q, want %q", gotHeaders.Get("Filename"), "snapshot.jpg")
	}
	if gotHeaders.Get("Message") != "Front door camera" {
		t.Errorf("Message header = %q, want %q", gotHeaders.Get("Message"), "Front door camera")
	}
	if gotHeaders.Get("Title") != "Motion detected" {
		t.Errorf("Title header = %q, want %q", gotHeaders.Get("Title"), "Motion detected")
	}
	if gotHeaders.Get("Click") != "https://camera.example.com/cameras/cam-1" {
		t.Errorf("Click header = %q, want %q", gotHeaders.Get("Click"), "https://camera.example.com/cameras/cam-1")
	}
}

func TestNtfySendWithThumbnailMessageFallsBackToTitle(t *testing.T) {
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "topic": "alerts"}
	notif := sdk.Notification{Title: "Motion detected", Thumbnail: []byte{0x01, 0x02}}

	if err := n.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if gotHeaders.Get("Message") != "Motion detected" {
		t.Errorf("Message header = %q, want fallback to title %q", gotHeaders.Get("Message"), "Motion detected")
	}
}

func TestNtfySendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "topic": "alerts"}
	notif := sdk.Notification{Title: "x"}

	err := n.Send(nil, cfg, notif)
	if err == nil {
		t.Fatalf("Send: got nil error, want error on non-2xx response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
}

func TestNtfyIDAndLabel(t *testing.T) {
	n := &ntfy{}
	if n.ID() != "ntfy" {
		t.Errorf("ID() = %q, want %q", n.ID(), "ntfy")
	}
	if n.Label() != "ntfy" {
		t.Errorf("Label() = %q, want %q", n.Label(), "ntfy")
	}
}

func TestNtfySchemaConditions(t *testing.T) {
	n := &ntfy{}
	schema := n.Schema()

	byKey := map[string]sdk.JsonSchema{}
	for _, f := range schema {
		byKey[f.Key] = f
		if len(f.Condition) != 1 || f.Condition[0].Key != "service" || f.Condition[0].Value != "ntfy" {
			t.Errorf("field %q: Condition = %+v, want gated on service==ntfy", f.Key, f.Condition)
		}
	}

	server, ok := byKey["ntfy_server"]
	if !ok {
		t.Fatalf("schema missing %q field", "ntfy_server")
	}
	if server.DefaultValue != "https://ntfy.sh" {
		t.Errorf("server DefaultValue = %v, want %q", server.DefaultValue, "https://ntfy.sh")
	}

	topic, ok := byKey["ntfy_topic"]
	if !ok {
		t.Fatalf("schema missing %q field", "ntfy_topic")
	}
	if !topic.Required {
		t.Errorf("topic Required = false, want true")
	}

	token, ok := byKey["ntfy_token"]
	if !ok {
		t.Fatalf("schema missing %q field", "ntfy_token")
	}
	if token.Format != sdk.StringFormatPassword {
		t.Errorf("token Format = %q, want %q", token.Format, sdk.StringFormatPassword)
	}
	if token.Required {
		t.Errorf("token Required = true, want false (optional)")
	}
}
