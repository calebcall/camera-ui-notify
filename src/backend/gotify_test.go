package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

func TestGotifyParseTargetMissingServer(t *testing.T) {
	g := &gotify{}

	if _, err := g.ParseTarget(map[string]any{"gotify_token": "tk_secret"}); err == nil {
		t.Fatalf("ParseTarget with no server: got nil error, want error")
	}
	if _, err := g.ParseTarget(map[string]any{"gotify_server": "", "gotify_token": "tk_secret"}); err == nil {
		t.Fatalf("ParseTarget with empty server: got nil error, want error")
	}
}

func TestGotifyParseTargetMissingToken(t *testing.T) {
	g := &gotify{}

	if _, err := g.ParseTarget(map[string]any{"gotify_server": "https://gotify.example.com"}); err == nil {
		t.Fatalf("ParseTarget with no token: got nil error, want error")
	}
	if _, err := g.ParseTarget(map[string]any{"gotify_server": "https://gotify.example.com", "gotify_token": ""}); err == nil {
		t.Fatalf("ParseTarget with empty token: got nil error, want error")
	}
}

func TestGotifyParseTargetTrimsTrailingSlash(t *testing.T) {
	g := &gotify{}

	cfg, err := g.ParseTarget(map[string]any{
		"gotify_server": "https://gotify.example.com/",
		"gotify_token":  "tk_secret",
	})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["server"] != "https://gotify.example.com" {
		t.Errorf("server = %q, want trailing slash trimmed", cfg["server"])
	}
	if cfg["token"] != "tk_secret" {
		t.Errorf("token = %q, want %q", cfg["token"], "tk_secret")
	}
}

// decodeGotifyPayload is a loose structural mirror of what gotify.Send is
// expected to emit, used only to assert on the JSON body in tests.
type decodedGotifyPayload struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
	Extras   *struct {
		ClientDisplay *struct {
			ContentType string `json:"contentType"`
		} `json:"client::display"`
		ClientNotification *struct {
			Click *struct {
				URL string `json:"url"`
			} `json:"click"`
			BigImageURL string `json:"bigImageUrl"`
		} `json:"client::notification"`
	} `json:"extras"`
}

func TestGotifySendBasic(t *testing.T) {
	var gotMethod, gotPath, gotRawQuery, gotHeaderToken string
	var gotBody decodedGotifyPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotRawQuery = r.URL.RawQuery
		gotHeaderToken = r.Header.Get("X-Gotify-Key")
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGotify()
	g.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "token": "tk_secret"}
	notif := sdk.Notification{Title: "Motion detected", Body: "Front door camera", Severity: sdk.SeverityInfo}

	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/message" {
		t.Errorf("path = %q, want /message", gotPath)
	}
	if gotHeaderToken != "tk_secret" {
		t.Errorf("X-Gotify-Key header = %q, want %q", gotHeaderToken, "tk_secret")
	}
	if strings.Contains(gotRawQuery, "tk_secret") {
		t.Errorf("request URL query = %q, want it to NOT contain the token", gotRawQuery)
	}
	if gotBody.Title != "Motion detected" {
		t.Errorf("title = %q, want %q", gotBody.Title, "Motion detected")
	}
	if gotBody.Message != "Front door camera" {
		t.Errorf("message = %q, want %q", gotBody.Message, "Front door camera")
	}
	if want := PriorityScale(sdk.SeverityInfo, 4, 10); gotBody.Priority != want {
		t.Errorf("priority = %d, want %d", gotBody.Priority, want)
	}
	if gotBody.Extras != nil {
		t.Errorf("extras = %+v, want nil when DeepLink and ImageURL unset", gotBody.Extras)
	}
}

func TestGotifySendBodyFallsBackToTitle(t *testing.T) {
	var gotBody decodedGotifyPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGotify()
	g.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "token": "tk_secret"}
	notif := sdk.Notification{Title: "Motion detected"}

	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if gotBody.Message != "Motion detected" {
		t.Errorf("message = %q, want fallback to title %q", gotBody.Message, "Motion detected")
	}
}

func TestGotifySendPriorityBySeverity(t *testing.T) {
	cases := []struct {
		sev  sdk.Severity
		want int
	}{
		{sdk.SeverityInfo, 4},
		{sdk.SeverityCritical, 10},
	}

	for _, tc := range cases {
		var gotBody decodedGotifyPayload
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.WriteHeader(http.StatusOK)
		}))

		g := newGotify()
		g.client = srv.Client()
		cfg := map[string]string{"server": srv.URL, "token": "tk_secret"}
		notif := sdk.Notification{Title: "x", Severity: tc.sev}

		if err := g.Send(nil, cfg, notif); err != nil {
			t.Fatalf("Send: unexpected error: %v", err)
		}
		if gotBody.Priority != tc.want {
			t.Errorf("severity %q: priority = %d, want %d", tc.sev, gotBody.Priority, tc.want)
		}
		srv.Close()
	}

	priorityFor := func(sev sdk.Severity) int {
		var gotBody decodedGotifyPayload
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		g := newGotify()
		g.client = srv.Client()
		cfg := map[string]string{"server": srv.URL, "token": "tk_secret"}
		_ = g.Send(nil, cfg, sdk.Notification{Title: "x", Severity: sev})
		return gotBody.Priority
	}

	info := priorityFor(sdk.SeverityInfo)
	warn := priorityFor(sdk.SeverityWarn)
	errP := priorityFor(sdk.SeverityError)
	crit := priorityFor(sdk.SeverityCritical)
	if !(info <= warn && warn <= errP && errP <= crit) {
		t.Fatalf("priorities not monotonic: info=%d warn=%d error=%d critical=%d", info, warn, errP, crit)
	}
}

func TestGotifySendExtrasWithDeepLinkAndImage(t *testing.T) {
	var gotBody decodedGotifyPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGotify()
	g.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "token": "tk_secret"}
	notif := sdk.Notification{
		Title:    "Motion detected",
		Body:     "Front door",
		Severity: sdk.SeverityWarn,
		DeepLink: "/cameras/cam-1",
		ImageURL: "https://example.com/snap.jpg",
	}

	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotBody.Extras == nil {
		t.Fatalf("extras = nil, want set when DeepLink and ImageURL present")
	}
	if gotBody.Extras.ClientDisplay == nil || gotBody.Extras.ClientDisplay.ContentType != "text/plain" {
		t.Errorf("client::display = %+v, want contentType text/plain", gotBody.Extras.ClientDisplay)
	}
	if gotBody.Extras.ClientNotification == nil {
		t.Fatalf("client::notification = nil, want set")
	}
	if gotBody.Extras.ClientNotification.Click == nil || gotBody.Extras.ClientNotification.Click.URL != "/cameras/cam-1" {
		t.Errorf("click = %+v, want url %q", gotBody.Extras.ClientNotification.Click, "/cameras/cam-1")
	}
	if gotBody.Extras.ClientNotification.BigImageURL != "https://example.com/snap.jpg" {
		t.Errorf("bigImageUrl = %q, want %q", gotBody.Extras.ClientNotification.BigImageURL, "https://example.com/snap.jpg")
	}
}

func TestGotifySendExtrasImageOnly(t *testing.T) {
	var gotBody decodedGotifyPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGotify()
	g.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "token": "tk_secret"}
	notif := sdk.Notification{
		Title:    "Motion detected",
		ImageURL: "https://example.com/snap.jpg",
	}

	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotBody.Extras == nil || gotBody.Extras.ClientNotification == nil {
		t.Fatalf("extras/client::notification = nil, want set when ImageURL present")
	}
	if gotBody.Extras.ClientNotification.BigImageURL != "https://example.com/snap.jpg" {
		t.Errorf("bigImageUrl = %q, want %q", gotBody.Extras.ClientNotification.BigImageURL, "https://example.com/snap.jpg")
	}
	if gotBody.Extras.ClientNotification.Click != nil {
		t.Errorf("click = %+v, want nil when DeepLink unset", gotBody.Extras.ClientNotification.Click)
	}
	if gotBody.Extras.ClientDisplay != nil {
		t.Errorf("client::display = %+v, want nil when DeepLink unset", gotBody.Extras.ClientDisplay)
	}
}

func TestGotifySendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	g := newGotify()
	g.client = srv.Client()

	cfg := map[string]string{"server": srv.URL, "token": "tk_secret"}
	notif := sdk.Notification{Title: "x"}

	err := g.Send(nil, cfg, notif)
	if err == nil {
		t.Fatalf("Send: got nil error, want error on non-2xx response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
}

func TestGotifyIDAndLabel(t *testing.T) {
	g := &gotify{}
	if g.ID() != "gotify" {
		t.Errorf("ID() = %q, want %q", g.ID(), "gotify")
	}
	if g.Label() != "Gotify" {
		t.Errorf("Label() = %q, want %q", g.Label(), "Gotify")
	}
}

func TestGotifySchemaConditions(t *testing.T) {
	g := &gotify{}
	schema := g.Schema()

	byKey := map[string]sdk.JsonSchema{}
	for _, f := range schema {
		byKey[f.Key] = f
		if len(f.Condition) != 1 || f.Condition[0].Key != "service" || f.Condition[0].Value != "gotify" {
			t.Errorf("field %q: Condition = %+v, want gated on service==gotify", f.Key, f.Condition)
		}
	}

	server, ok := byKey["gotify_server"]
	if !ok {
		t.Fatalf("schema missing %q field", "gotify_server")
	}
	if !server.Required {
		t.Errorf("server Required = false, want true")
	}

	token, ok := byKey["gotify_token"]
	if !ok {
		t.Fatalf("schema missing %q field", "gotify_token")
	}
	if token.Format != sdk.StringFormatPassword {
		t.Errorf("token Format = %q, want %q", token.Format, sdk.StringFormatPassword)
	}
	if !token.Required {
		t.Errorf("token Required = false, want true")
	}
	if token.Title != "Application token" {
		t.Errorf("token Title = %q, want %q", token.Title, "Application token")
	}
}
