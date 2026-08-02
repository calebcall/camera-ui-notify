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

type decodedGrafanaAlert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
}

func TestGrafanaAlertsParseRequiresServerAndToken(t *testing.T) {
	a := newGrafanaAlerts()
	if _, err := a.parse(map[string]any{"grafana_token": "tk"}); err == nil {
		t.Errorf("missing server: got nil error, want error")
	}
	if _, err := a.parse(map[string]any{"grafana_server": "https://g.example"}); err == nil {
		t.Errorf("missing token: got nil error, want error")
	}
}

func TestGrafanaAlertsParseDefaults(t *testing.T) {
	cfg, err := newGrafanaAlerts().parse(map[string]any{
		"grafana_server": "https://g.example",
		"grafana_token":  "tk",
	})
	if err != nil {
		t.Fatalf("parse: unexpected error: %v", err)
	}
	if cfg["alertname"] != grafanaDefaultAlertname {
		t.Errorf("alertname = %q, want %q", cfg["alertname"], grafanaDefaultAlertname)
	}
	if cfg["ttl"] != "300" {
		t.Errorf("ttl = %q, want %q", cfg["ttl"], "300")
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

func TestGrafanaAlertsSchemaGating(t *testing.T) {
	byKey := map[string]sdk.JsonSchema{}
	for _, f := range newGrafanaAlerts().schema() {
		byKey[f.Key] = f
		if len(f.Condition) != 2 || f.Condition[1].Key != "grafana_mode" || f.Condition[1].Value != grafanaModeAlerts {
			t.Errorf("field %q: Condition = %+v, want gated on grafana_mode==alerts", f.Key, f.Condition)
		}
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
}

// sendOneAlert runs a single alerts-mode Send against an httptest server and
// returns the decoded first (and only) alert of the posted array.
func sendOneAlert(t *testing.T, cfg map[string]string, notif sdk.Notification) (decodedGrafanaAlert, string) {
	t.Helper()

	var gotPath string
	var gotBody []decodedGrafanaAlert

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	full := map[string]string{"mode": grafanaModeAlerts, "server": srv.URL, "token": "tk",
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
	return gotBody[0], gotPath
}

func TestGrafanaAlertsSendBasic(t *testing.T) {
	notif := sdk.Notification{
		Title:    "Person detected",
		Body:     "Someone is at the door",
		Severity: sdk.SeverityWarn,
		ImageURL: "https://cam.example/snap.jpg",
		DeepLink: "https://cam.example/cameras/driveway",
		Data:     map[string]string{"cameraId": "driveway", "eventId": "evt-42"},
	}

	got, gotPath := sendOneAlert(t, nil, notif)

	if gotPath != "/api/alertmanager/grafana/api/v2/alerts" {
		t.Errorf("path = %q, want /api/alertmanager/grafana/api/v2/alerts", gotPath)
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
		t.Errorf("annotations.summary = %q, want %q", got.Annotations["summary"], "Person detected")
	}
	if got.Annotations["description"] != "Someone is at the door" {
		t.Errorf("annotations.description = %q, want %q", got.Annotations["description"], "Someone is at the door")
	}
	if got.Annotations["image_url"] != "https://cam.example/snap.jpg" {
		t.Errorf("annotations.image_url = %q, want the snapshot URL", got.Annotations["image_url"])
	}
	if got.GeneratorURL != "https://cam.example/cameras/driveway" {
		t.Errorf("generatorURL = %q, want the absolute deep link", got.GeneratorURL)
	}
}

func TestGrafanaAlertsSendEndsAtIsStartPlusTTL(t *testing.T) {
	got, _ := sendOneAlert(t, map[string]string{"ttl": "600"}, sdk.Notification{Title: "x"})

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

func TestGrafanaAlertsSendOmitsEmptyFields(t *testing.T) {
	got, _ := sendOneAlert(t, nil, sdk.Notification{Title: "Plugin updated"})

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

func TestGrafanaAlertsSendDropsRelativeDeepLink(t *testing.T) {
	got, _ := sendOneAlert(t, nil, sdk.Notification{Title: "x", DeepLink: "/cameras/cam-1"})
	if got.GeneratorURL != "" {
		t.Errorf("generatorURL = %q, want empty for a router-relative deep link", got.GeneratorURL)
	}
}

func TestGrafanaAlertsSendGeneratesUniqueEventIDPerEvent(t *testing.T) {
	a, _ := sendOneAlert(t, nil, sdk.Notification{Title: "x"})
	b, _ := sendOneAlert(t, nil, sdk.Notification{Title: "x"})

	if a.Labels["event_id"] == "" || b.Labels["event_id"] == "" {
		t.Fatalf("event_id empty: a=%q b=%q", a.Labels["event_id"], b.Labels["event_id"])
	}
	if a.Labels["event_id"] == b.Labels["event_id"] {
		t.Errorf("event_id = %q for both sends, want distinct ids so Alertmanager does not dedupe them",
			a.Labels["event_id"])
	}
}

func TestGrafanaAlertsSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid label"))
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{"mode": grafanaModeAlerts, "server": srv.URL, "token": "tk",
		"alertname": grafanaDefaultAlertname, "ttl": "300"}
	err := g.Send(nil, cfg, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatalf("got nil error, want error on 400")
	}
	if !strings.HasPrefix(err.Error(), "grafana: alerts: ") {
		t.Errorf("error = %q, want the %q prefix", err.Error(), "grafana: alerts: ")
	}
}
