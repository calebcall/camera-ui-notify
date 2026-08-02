package backend

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

type decodedGrafanaAlert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
}

func TestGrafanaAlertmanagerParseRequiresURL(t *testing.T) {
	a := newGrafanaAlertmanager()
	if _, err := a.parse(map[string]any{}); err == nil {
		t.Errorf("missing url: got nil error, want error")
	}
	if _, err := a.parse(map[string]any{"grafana_am_url": "   "}); err == nil {
		t.Errorf("blank url: got nil error, want error")
	}
}

// The alertmanager mode addresses an Alertmanager directly, so it must not
// demand the Grafana-instance fields the annotations mode uses.
func TestGrafanaAlertmanagerParseDoesNotRequireServerOrToken(t *testing.T) {
	cfg, err := newGrafanaAlertmanager().parse(map[string]any{
		"grafana_am_url": "  https://alertmanager.example.com/  ",
	})
	if err != nil {
		t.Fatalf("parse: unexpected error: %v", err)
	}
	if cfg["url"] != "https://alertmanager.example.com" {
		t.Errorf("url = %q, want it trimmed with no trailing slash", cfg["url"])
	}
	if cfg["alertname"] != grafanaDefaultAlertname {
		t.Errorf("alertname = %q, want %q", cfg["alertname"], grafanaDefaultAlertname)
	}
	if cfg["ttl"] != "300" {
		t.Errorf("ttl = %q, want %q", cfg["ttl"], "300")
	}
	if cfg["user"] != "" || cfg["password"] != "" {
		t.Errorf("user/password = %q/%q, want both empty when unset", cfg["user"], cfg["password"])
	}
}

func TestGrafanaAlertmanagerParseRejectsHalfConfiguredBasicAuth(t *testing.T) {
	a := newGrafanaAlertmanager()
	base := "https://alertmanager.example.com"

	if _, err := a.parse(map[string]any{"grafana_am_url": base, "grafana_am_user": "u"}); err == nil {
		t.Errorf("username without password: got nil error, want error")
	}
	if _, err := a.parse(map[string]any{"grafana_am_url": base, "grafana_am_password": "p"}); err == nil {
		t.Errorf("password without username: got nil error, want error")
	}

	cfg, err := a.parse(map[string]any{
		"grafana_am_url": base, "grafana_am_user": " u ", "grafana_am_password": " p ",
	})
	if err != nil {
		t.Fatalf("both set: unexpected error: %v", err)
	}
	if cfg["user"] != "u" || cfg["password"] != "p" {
		t.Errorf("user/password = %q/%q, want them trimmed to u/p", cfg["user"], cfg["password"])
	}
}

func TestGrafanaParseTTLCoercion(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		{"nil falls back to default", nil, grafanaDefaultTTL},
		{"empty string falls back to default", "", grafanaDefaultTTL},
		{"blank string falls back to default", "   ", grafanaDefaultTTL},
		{"unparseable string falls back to default", "abc", grafanaDefaultTTL},
		{"float64 from the UI", float64(600), 600},
		{"int", 600, 600},
		{"int64", int64(600), 600},
		{"numeric string", "600", 600},
		{"below minimum clamps", float64(5), grafanaMinTTL},
		{"zero clamps", 0, grafanaMinTTL},
		{"negative clamps", -10, grafanaMinTTL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := grafanaParseTTL(tc.in); got != tc.want {
				t.Errorf("grafanaParseTTL(%#v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestGrafanaAlertmanagerSchemaGating(t *testing.T) {
	byKey := map[string]sdk.JsonSchema{}
	for _, f := range newGrafanaAlertmanager().schema() {
		byKey[f.Key] = f
		if len(f.Condition) != 2 || f.Condition[1].Key != "grafana_mode" || f.Condition[1].Value != grafanaModeAlertmanager {
			t.Errorf("field %q: Condition = %+v, want gated on grafana_mode==alertmanager", f.Key, f.Condition)
		}
		if !strings.HasPrefix(f.Key, "grafana_") {
			t.Errorf("field key %q: want a grafana_ prefix", f.Key)
		}
	}

	amURL, ok := byKey["grafana_am_url"]
	if !ok {
		t.Fatalf("schema missing %q field", "grafana_am_url")
	}
	if !amURL.Required {
		t.Errorf("grafana_am_url Required = false, want true")
	}

	pw, ok := byKey["grafana_am_password"]
	if !ok {
		t.Fatalf("schema missing %q field", "grafana_am_password")
	}
	if pw.Format != sdk.StringFormatPassword {
		t.Errorf("grafana_am_password Format = %q, want %q", pw.Format, sdk.StringFormatPassword)
	}
	if pw.Required {
		t.Errorf("grafana_am_password Required = true, want false (basic auth is optional)")
	}

	name, ok := byKey["grafana_alertname"]
	if !ok {
		t.Fatalf("schema missing %q field", "grafana_alertname")
	}
	if name.DefaultValue != grafanaDefaultAlertname {
		t.Errorf("grafana_alertname DefaultValue = %v, want %q", name.DefaultValue, grafanaDefaultAlertname)
	}

	ttl, ok := byKey["grafana_ttl"]
	if !ok {
		t.Fatalf("schema missing %q field", "grafana_ttl")
	}
	if ttl.Type != sdk.JsonSchemaTypeNumber {
		t.Errorf("grafana_ttl Type = %q, want %q", ttl.Type, sdk.JsonSchemaTypeNumber)
	}
	if ttl.DefaultValue != grafanaDefaultTTL {
		t.Errorf("grafana_ttl DefaultValue = %v, want %d", ttl.DefaultValue, grafanaDefaultTTL)
	}
	if ttl.Minimum == nil || *ttl.Minimum != float64(grafanaMinTTL) {
		t.Errorf("grafana_ttl Minimum = %v, want %d", ttl.Minimum, grafanaMinTTL)
	}
	// Step is deliberately unset: min=30 step=30 makes an HTML5 number input
	// reject legitimate values like 100 or 450.
	if ttl.Step != nil {
		t.Errorf("grafana_ttl Step = %v, want it unset", *ttl.Step)
	}

	// The Grafana-instance fields belong to the annotations mode only.
	for _, k := range []string{"grafana_server", "grafana_token"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("alertmanager schema declares %q, want it left to the annotations mode", k)
		}
	}
}

// sendOneAlert runs a single alertmanager-mode Send against an httptest
// server and returns the decoded first (and only) alert, the request path,
// and the Authorization header.
func sendOneAlert(t *testing.T, cfg map[string]string, notif sdk.Notification) (decodedGrafanaAlert, string, string) {
	t.Helper()

	var gotPath, gotAuth string
	var gotBody []decodedGrafanaAlert

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	full := map[string]string{"mode": grafanaModeAlertmanager, "url": srv.URL,
		"alertname": grafanaDefaultAlertname, "ttl": "300"}
	for k, v := range cfg {
		full[k] = v
	}

	if err := g.Send(nil, full, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if len(gotBody) != 1 {
		t.Fatalf("posted %d alerts, want exactly 1", len(gotBody))
	}
	return gotBody[0], gotPath, gotAuth
}

// The endpoint is Alertmanager's own v2 API. Grafana's built-in Alertmanager
// has no POST route for alerts (only {DatasourceUID} does), which is what
// made the previous {server}/api/alertmanager/grafana/... target fail with
// 400 "bad request data" — see #33.
func TestGrafanaAlertmanagerSendPostsToAlertmanagerV2API(t *testing.T) {
	notif := sdk.Notification{
		Title:    "Person detected",
		Body:     "Someone is at the door",
		Severity: sdk.SeverityWarn,
		ImageURL: "https://cam.example/snap.jpg",
		DeepLink: "https://cam.example/cameras/driveway",
		Data:     map[string]string{"cameraId": "driveway", "eventId": "evt-42"},
	}

	got, gotPath, gotAuth := sendOneAlert(t, nil, notif)

	if gotPath != "/api/v2/alerts" {
		t.Errorf("path = %q, want /api/v2/alerts", gotPath)
	}
	if strings.Contains(gotPath, "alertmanager/grafana") {
		t.Errorf("path = %q, want it to NOT go through Grafana's alertmanager proxy", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none when no basic auth is configured", gotAuth)
	}

	wantLabels := map[string]string{
		"alertname": grafanaDefaultAlertname,
		"source":    "camera.ui",
		"severity":  "warn",
		"camera":    "driveway",
		"event_id":  "evt-42",
	}
	for k, want := range wantLabels {
		if got.Labels[k] != want {
			t.Errorf("labels[%q] = %q, want %q", k, got.Labels[k], want)
		}
	}
	if got.Annotations["summary"] != "Person detected" {
		t.Errorf("annotations.summary = %q, want the title", got.Annotations["summary"])
	}
	if got.Annotations["description"] != "Someone is at the door" {
		t.Errorf("annotations.description = %q, want the body", got.Annotations["description"])
	}
	if got.Annotations["image_url"] != "https://cam.example/snap.jpg" {
		t.Errorf("annotations.image_url = %q, want the snapshot URL", got.Annotations["image_url"])
	}
	if got.GeneratorURL != "https://cam.example/cameras/driveway" {
		t.Errorf("generatorURL = %q, want the absolute deep link", got.GeneratorURL)
	}
}

func TestGrafanaAlertmanagerSendUsesBasicAuthWhenConfigured(t *testing.T) {
	_, _, gotAuth := sendOneAlert(t,
		map[string]string{"user": "12345", "password": "glc_token"},
		sdk.Notification{Title: "x"})

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("12345:glc_token"))
	if gotAuth != want {
		t.Errorf("Authorization = %q, want %q", gotAuth, want)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(gotAuth, "Basic "))
	if err != nil {
		t.Fatalf("Authorization is not valid base64: %v", err)
	}
	if string(decoded) != "12345:glc_token" {
		t.Errorf("decoded credentials = %q, want %q", decoded, "12345:glc_token")
	}
}

func TestGrafanaAlertmanagerSendEndsAtIsStartPlusTTL(t *testing.T) {
	got, _, _ := sendOneAlert(t, map[string]string{"ttl": "600"}, sdk.Notification{Title: "x"})

	start, err := time.Parse(time.RFC3339, got.StartsAt)
	if err != nil {
		t.Fatalf("startsAt %q is not RFC3339: %v", got.StartsAt, err)
	}
	end, err := time.Parse(time.RFC3339, got.EndsAt)
	if err != nil {
		t.Fatalf("endsAt %q is not RFC3339: %v", got.EndsAt, err)
	}
	if d := end.Sub(start); d != 600*time.Second {
		t.Errorf("endsAt - startsAt = %v, want 600s", d)
	}
}

func TestGrafanaAlertmanagerSendOmitsEmptyFields(t *testing.T) {
	got, _, _ := sendOneAlert(t, nil, sdk.Notification{Title: "Plugin updated"})

	if _, ok := got.Labels["camera"]; ok {
		t.Errorf("labels contains camera = %q, want it omitted with no cameraId", got.Labels["camera"])
	}
	if _, ok := got.Annotations["description"]; ok {
		t.Errorf("annotations contains description, want it omitted with an empty Body")
	}
	if _, ok := got.Annotations["image_url"]; ok {
		t.Errorf("annotations contains image_url, want it omitted with no ImageURL")
	}
	if got.GeneratorURL != "" {
		t.Errorf("generatorURL = %q, want empty with no deep link", got.GeneratorURL)
	}
}

func TestGrafanaAlertmanagerSendDropsRelativeDeepLink(t *testing.T) {
	got, _, _ := sendOneAlert(t, nil, sdk.Notification{Title: "x", DeepLink: "/cameras/cam-1"})
	if got.GeneratorURL != "" {
		t.Errorf("generatorURL = %q, want empty for a router-relative deep link", got.GeneratorURL)
	}
}

func TestGrafanaAlertmanagerSendGeneratesUniqueEventIDPerEvent(t *testing.T) {
	a, _, _ := sendOneAlert(t, nil, sdk.Notification{Title: "x"})
	b, _, _ := sendOneAlert(t, nil, sdk.Notification{Title: "x"})

	if a.Labels["event_id"] == "" || b.Labels["event_id"] == "" {
		t.Fatalf("event_id empty: a=%q b=%q", a.Labels["event_id"], b.Labels["event_id"])
	}
	if a.Labels["event_id"] == b.Labels["event_id"] {
		t.Errorf("event_id = %q for both sends, want distinct ids so Alertmanager does not dedupe them",
			a.Labels["event_id"])
	}
}

func TestGrafanaAlertmanagerSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid label"))
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{"mode": grafanaModeAlertmanager, "url": srv.URL,
		"alertname": grafanaDefaultAlertname, "ttl": "300"}
	err := g.Send(nil, cfg, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatalf("got nil error, want error on 400")
	}
	if !strings.HasPrefix(err.Error(), "grafana: alertmanager: ") {
		t.Errorf("error = %q, want the %q prefix", err.Error(), "grafana: alertmanager: ")
	}
}

func TestGrafanaAlertmanagerSendErrorDoesNotLeakCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{"mode": grafanaModeAlertmanager, "url": srv.URL,
		"user": "12345", "password": "sup3rs3cr3t", "alertname": "x", "ttl": "300"}
	err := g.Send(nil, cfg, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatalf("got nil error, want error on 401")
	}
	if strings.Contains(err.Error(), "sup3rs3cr3t") {
		t.Errorf("error = %q, want the basic-auth password kept out of it", err.Error())
	}
}

// #34: the camera label should be readable. Data["cameraId"] is a UUID;
// camera.ui routes cameras by display name, so the deep link carries one.
func TestGrafanaAlertmanagerCameraLabelPrefersReadableName(t *testing.T) {
	got, _, _ := sendOneAlert(t, nil, sdk.Notification{
		Title:    "Patio — Audio",
		DeepLink: "https://cam.example/cameras/Patio?startTs=123",
		Data:     map[string]string{"cameraId": "07614b1d-d5de-48b7-bbb2-592a64a97ead"},
	})

	if got.Labels["camera"] != "Patio" {
		t.Errorf("labels.camera = %q, want the readable name from the deep link", got.Labels["camera"])
	}
	if got.Labels["camera_id"] != "07614b1d-d5de-48b7-bbb2-592a64a97ead" {
		t.Errorf("labels.camera_id = %q, want the raw UUID retained for rename-proof routing", got.Labels["camera_id"])
	}
}

func TestGrafanaAlertmanagerCameraIDOmittedWhenItRepeatsTheLabel(t *testing.T) {
	got, _, _ := sendOneAlert(t, nil, sdk.Notification{
		Title: "x",
		Data:  map[string]string{"cameraId": "driveway"},
	})

	if got.Labels["camera"] != "driveway" {
		t.Errorf("labels.camera = %q, want the id fallback", got.Labels["camera"])
	}
	if _, ok := got.Labels["camera_id"]; ok {
		t.Errorf("labels.camera_id present, want it omitted when identical to camera")
	}
}
