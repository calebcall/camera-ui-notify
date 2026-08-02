package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

type decodedGrafanaIRM struct {
	AlertUID string `json:"alert_uid"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	ImageURL string `json:"image_url"`
	Link     string `json:"link_to_upstream_details"`
	State    string `json:"state"`

	// Grafana Alerting webhook envelope — what a grafana_alerting
	// integration's templates read.
	Receiver          string                   `json:"receiver"`
	Status            string                   `json:"status"`
	Alerts            []decodedGrafanaIRMAlert `json:"alerts"`
	GroupLabels       map[string]string        `json:"groupLabels"`
	CommonLabels      map[string]string        `json:"commonLabels"`
	CommonAnnotations map[string]string        `json:"commonAnnotations"`
	ExternalURL       string                   `json:"externalURL"`
	Version           string                   `json:"version"`
	GroupKey          string                   `json:"groupKey"`
	TruncatedAlerts   int                      `json:"truncatedAlerts"`
}

type decodedGrafanaIRMAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
	ImageURL     string            `json:"imageURL"`
}

// sendOneIRM runs a single IRM-mode Send against an httptest server and
// returns the decoded body.
func sendOneIRM(t *testing.T, notif sdk.Notification) decodedGrafanaIRM {
	t.Helper()

	var got decodedGrafanaIRM
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	if err := g.Send(nil, map[string]string{"mode": grafanaModeIRM, "url": srv.URL}, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	return got
}

func TestGrafanaIRMParseRequiresURL(t *testing.T) {
	i := newGrafanaIRM()
	if _, err := i.parse(map[string]any{}); err == nil {
		t.Errorf("missing url: got nil error, want error")
	}
	if _, err := i.parse(map[string]any{"grafana_irm_url": "   "}); err == nil {
		t.Errorf("blank url: got nil error, want error")
	}
}

func TestGrafanaIRMParseDoesNotRequireServerOrToken(t *testing.T) {
	cfg, err := newGrafanaIRM().parse(map[string]any{
		"grafana_irm_url": "  https://oncall.example/integrations/v1/webhook/tk/  ",
	})
	if err != nil {
		t.Fatalf("parse: unexpected error: %v", err)
	}
	if cfg["url"] != "https://oncall.example/integrations/v1/webhook/tk/" {
		t.Errorf("url = %q, want it trimmed and otherwise untouched", cfg["url"])
	}
}

func TestGrafanaIRMSchemaGating(t *testing.T) {
	fields := newGrafanaIRM().schema()
	if len(fields) != 1 {
		t.Fatalf("schema has %d fields, want 1", len(fields))
	}
	f := fields[0]
	if f.Key != "grafana_irm_url" {
		t.Errorf("key = %q, want %q", f.Key, "grafana_irm_url")
	}
	if !f.Required {
		t.Errorf("Required = false, want true")
	}
	if f.Format != sdk.StringFormatPassword {
		t.Errorf("Format = %q, want %q (the URL embeds the credential)", f.Format, sdk.StringFormatPassword)
	}
	want := grafanaModeCondition(grafanaModeIRM)
	if !reflect.DeepEqual(f.Condition, want) {
		t.Errorf("Condition = %+v, want %+v", f.Condition, want)
	}
}

func TestGrafanaIRMSendBasic(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody decodedGrafanaIRM

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
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

	cfg := map[string]string{"mode": grafanaModeIRM, "url": srv.URL + "/integrations/v1/webhook/tk/"}
	notif := sdk.Notification{
		Title:    "Person detected",
		Body:     "Someone is at the door",
		ImageURL: "https://cam.example/snap.jpg",
		DeepLink: "https://cam.example/cameras/driveway",
		Data:     map[string]string{"eventId": "evt-42"},
	}

	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/integrations/v1/webhook/tk/" {
		t.Errorf("path = %q, want the integration URL path", gotPath)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want none (the credential is in the URL)", gotAuth)
	}
	if gotBody.AlertUID != "evt-42" {
		t.Errorf("alert_uid = %q, want %q", gotBody.AlertUID, "evt-42")
	}
	if gotBody.Title != "Person detected" {
		t.Errorf("title = %q, want %q", gotBody.Title, "Person detected")
	}
	if gotBody.Message != "Someone is at the door" {
		t.Errorf("message = %q, want %q", gotBody.Message, "Someone is at the door")
	}
	if gotBody.ImageURL != "https://cam.example/snap.jpg" {
		t.Errorf("image_url = %q, want the snapshot URL", gotBody.ImageURL)
	}
	if gotBody.Link != "https://cam.example/cameras/driveway" {
		t.Errorf("link_to_upstream_details = %q, want the absolute deep link", gotBody.Link)
	}
	if gotBody.State != "alerting" {
		t.Errorf("state = %q, want %q", gotBody.State, "alerting")
	}
}

func TestGrafanaIRMSendMessageFallsBackToTitle(t *testing.T) {
	var gotBody decodedGrafanaIRM

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{"mode": grafanaModeIRM, "url": srv.URL}
	if err := g.Send(nil, cfg, sdk.Notification{Title: "Camera offline", DeepLink: "/cameras/cam-1"}); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotBody.Message != "Camera offline" {
		t.Errorf("message = %q, want fallback to the title", gotBody.Message)
	}
	if gotBody.ImageURL != "" {
		t.Errorf("image_url = %q, want omitted with no ImageURL", gotBody.ImageURL)
	}
	if gotBody.Link != "" {
		t.Errorf("link_to_upstream_details = %q, want omitted for a router-relative deep link", gotBody.Link)
	}
}

func TestGrafanaIRMSendNon2xxDoesNotLeakIntegrationURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("integration not found"))
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{"mode": grafanaModeIRM, "url": srv.URL + "/integrations/v1/webhook/sup3rs3cr3t/"}
	err := g.Send(nil, cfg, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatalf("got nil error, want error on 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %q, want it to mention status 404", err.Error())
	}
	if strings.Contains(err.Error(), "sup3rs3cr3t") {
		t.Errorf("error = %q, want the integration URL token kept out of it", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "grafana: irm: ") {
		t.Errorf("error = %q, want the %q prefix", err.Error(), "grafana: irm: ")
	}
}

func TestGrafanaIRMSendTransportErrorDoesNotLeakIntegrationURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := srv.Client()
	url := srv.URL + "/integrations/v1/webhook/sup3rs3cr3t/"
	srv.Close() // force a connection-refused transport error

	g := newGrafana()
	g.client = client

	err := g.Send(nil, map[string]string{"mode": grafanaModeIRM, "url": url}, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatalf("got nil error, want a transport error")
	}
	if strings.Contains(err.Error(), "sup3rs3cr3t") {
		t.Errorf("error = %q, want the integration URL redacted out", err.Error())
	}
}

// The tests below cover the Grafana Alerting webhook envelope added in
// response to #31: a grafana_alerting integration renders alert groups with
// templates that read payload.status and payload.alerts[], and 0.6.0 sent
// neither, producing "'dict object' has no attribute 'alerts'".

func TestGrafanaIRMSendEmitsAlertingEnvelope(t *testing.T) {
	got := sendOneIRM(t, sdk.Notification{
		Title:    "Patio — Person",
		Body:     "Person 100% · person",
		Severity: sdk.SeverityWarn,
		ImageURL: "https://cam.example/snap.jpg",
		DeepLink: "https://cam.example/cameras/Patio?startTs=1785642365558",
		Data:     map[string]string{"cameraId": "Patio", "eventId": "evt-42"},
	})

	if got.Status != "firing" {
		t.Errorf("status = %q, want %q", got.Status, "firing")
	}
	if got.Receiver != "camera.ui" {
		t.Errorf("receiver = %q, want %q", got.Receiver, "camera.ui")
	}
	if got.Version != "1" {
		t.Errorf("version = %q, want %q", got.Version, "1")
	}
	if got.TruncatedAlerts != 0 {
		t.Errorf("truncatedAlerts = %d, want 0", got.TruncatedAlerts)
	}
	if len(got.Alerts) != 1 {
		t.Fatalf("alerts has %d entries, want exactly 1", len(got.Alerts))
	}

	a := got.Alerts[0]
	if a.Status != "firing" {
		t.Errorf("alerts[0].status = %q, want %q", a.Status, "firing")
	}
	wantLabels := map[string]string{
		"alertname": grafanaDefaultAlertname,
		"source":    "camera.ui",
		"severity":  "warn",
		"camera":    "Patio",
	}
	if !reflect.DeepEqual(a.Labels, wantLabels) {
		t.Errorf("alerts[0].labels = %v, want %v", a.Labels, wantLabels)
	}
	if a.Annotations["summary"] != "Patio — Person" {
		t.Errorf("alerts[0].annotations.summary = %q, want the title", a.Annotations["summary"])
	}
	if a.Annotations["description"] != "Person 100% · person" {
		t.Errorf("alerts[0].annotations.description = %q, want the body", a.Annotations["description"])
	}
	if a.Fingerprint != "evt-42" {
		t.Errorf("alerts[0].fingerprint = %q, want the event id", a.Fingerprint)
	}
	if a.GeneratorURL != "https://cam.example/cameras/Patio?startTs=1785642365558" {
		t.Errorf("alerts[0].generatorURL = %q, want the absolute deep link", a.GeneratorURL)
	}
	if a.ImageURL != "https://cam.example/snap.jpg" {
		t.Errorf("alerts[0].imageURL = %q, want the snapshot URL", a.ImageURL)
	}
	if _, err := time.Parse(time.RFC3339, a.StartsAt); err != nil {
		t.Errorf("alerts[0].startsAt = %q, not RFC3339: %v", a.StartsAt, err)
	}
	if a.EndsAt != grafanaIRMUnresolvedEndsAt {
		t.Errorf("alerts[0].endsAt = %q, want the unresolved sentinel %q", a.EndsAt, grafanaIRMUnresolvedEndsAt)
	}

	if !reflect.DeepEqual(got.CommonLabels, wantLabels) {
		t.Errorf("commonLabels = %v, want the alert's labels %v", got.CommonLabels, wantLabels)
	}
	if got.CommonAnnotations["summary"] != "Patio — Person" {
		t.Errorf("commonAnnotations.summary = %q, want the title", got.CommonAnnotations["summary"])
	}
}

func TestGrafanaIRMGroupKeyIsPerCamera(t *testing.T) {
	got := sendOneIRM(t, sdk.Notification{
		Title: "Patio — Person",
		Data:  map[string]string{"cameraId": "Patio"},
	})
	if got.GroupKey != "camera.ui:Patio" {
		t.Errorf("groupKey = %q, want %q", got.GroupKey, "camera.ui:Patio")
	}
	wantGroupLabels := map[string]string{"alertname": grafanaDefaultAlertname, "camera": "Patio"}
	if !reflect.DeepEqual(got.GroupLabels, wantGroupLabels) {
		t.Errorf("groupLabels = %v, want %v", got.GroupLabels, wantGroupLabels)
	}
}

func TestGrafanaIRMGroupKeyFallsBackWithoutCamera(t *testing.T) {
	got := sendOneIRM(t, sdk.Notification{Title: "Plugin updated"})

	if got.GroupKey != "camera.ui" {
		t.Errorf("groupKey = %q, want %q for a non-camera notification", got.GroupKey, "camera.ui")
	}
	wantGroupLabels := map[string]string{"alertname": grafanaDefaultAlertname}
	if !reflect.DeepEqual(got.GroupLabels, wantGroupLabels) {
		t.Errorf("groupLabels = %v, want %v", got.GroupLabels, wantGroupLabels)
	}
	if _, ok := got.Alerts[0].Labels["camera"]; ok {
		t.Errorf("alerts[0].labels has a camera key, want it omitted with no cameraId")
	}
}

func TestGrafanaIRMTwoCamerasGetDistinctGroupKeys(t *testing.T) {
	patio := sendOneIRM(t, sdk.Notification{Title: "a", Data: map[string]string{"cameraId": "Patio"}})
	drive := sendOneIRM(t, sdk.Notification{Title: "b", Data: map[string]string{"cameraId": "Driveway"}})

	if patio.GroupKey == drive.GroupKey {
		t.Errorf("both cameras produced groupKey %q, want one group per camera", patio.GroupKey)
	}
}

func TestGrafanaIRMExternalURLDerivedFromDeepLink(t *testing.T) {
	got := sendOneIRM(t, sdk.Notification{
		Title:    "x",
		DeepLink: "https://cameraui.example.com/cameras/Patio?startTs=123",
	})
	if got.ExternalURL != "https://cameraui.example.com" {
		t.Errorf("externalURL = %q, want the deep link's scheme+host", got.ExternalURL)
	}
}

func TestGrafanaIRMExternalURLEmptyForRelativeDeepLink(t *testing.T) {
	got := sendOneIRM(t, sdk.Notification{Title: "x", DeepLink: "/cameras/Patio"})

	if got.ExternalURL != "" {
		t.Errorf("externalURL = %q, want empty when the deep link is relative", got.ExternalURL)
	}
	if got.Alerts[0].GeneratorURL != "" {
		t.Errorf("alerts[0].generatorURL = %q, want empty for a relative deep link", got.Alerts[0].GeneratorURL)
	}
}

func TestGrafanaIRMKeepsFormattedWebhookFields(t *testing.T) {
	// The envelope is additive: a formatted-webhook ("webhook" type)
	// integration must keep rendering, so 0.6.0's field set stays.
	got := sendOneIRM(t, sdk.Notification{
		Title:    "Patio — Person",
		Body:     "Person 100%",
		ImageURL: "https://cam.example/snap.jpg",
		DeepLink: "https://cam.example/cameras/Patio",
		Data:     map[string]string{"eventId": "evt-42"},
	})

	if got.AlertUID != "evt-42" {
		t.Errorf("alert_uid = %q, want it retained", got.AlertUID)
	}
	if got.Title != "Patio — Person" || got.Message != "Person 100%" || got.State != "alerting" {
		t.Errorf("title/message/state = %q/%q/%q, want them retained", got.Title, got.Message, got.State)
	}
	if got.ImageURL != "https://cam.example/snap.jpg" {
		t.Errorf("image_url = %q, want it retained", got.ImageURL)
	}
	if got.Link != "https://cam.example/cameras/Patio" {
		t.Errorf("link_to_upstream_details = %q, want it retained", got.Link)
	}
}
