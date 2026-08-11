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

// This file covers how the Grafana backend handles the republish camera.ui
// sends once an AI description is ready: same Tag, plus Silent. Every mode
// revises what it already filed rather than opening a second record —
// annotations by PATCHing, the two alert surfaces by re-filing under the
// event id the original alert used.

// grafanaRecorder captures the requests a mode makes against a fake Grafana.
type grafanaRecorder struct {
	methods []string
	paths   []string
	bodies  [][]byte
}

// newGrafanaRecorder returns a recorder plus a server that answers every
// request with the given JSON body.
func newGrafanaRecorder(t *testing.T, response string) (*grafanaRecorder, *httptest.Server) {
	t.Helper()

	rec := &grafanaRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.methods = append(rec.methods, r.Method)
		rec.paths = append(rec.paths, r.URL.Path)
		rec.bodies = append(rec.bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

// --- annotations ----------------------------------------------------------

func TestGrafanaAnnotationsSilentPatchesExisting(t *testing.T) {
	rec, srv := newGrafanaRecorder(t, `{"message":"Annotation added","id":77}`)

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{
		"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk", "tags": "",
	}

	if err := g.Send(nil, cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if err := g.Send(nil, cfg, silentNotification()); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(rec.methods) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(rec.methods))
	}
	if rec.methods[0] != http.MethodPost || rec.paths[0] != "/api/annotations" {
		t.Errorf("first request = %s %s, want POST /api/annotations", rec.methods[0], rec.paths[0])
	}
	// The follow-up revises annotation 77 rather than dropping a second
	// marker on the dashboard a few seconds after the first.
	if rec.methods[1] != http.MethodPatch {
		t.Fatalf("second request method = %s, want PATCH", rec.methods[1])
	}
	if want := "/api/annotations/77"; rec.paths[1] != want {
		t.Errorf("patch path = %q, want %q", rec.paths[1], want)
	}

	var patch struct {
		Text string   `json:"text"`
		Tags []string `json:"tags"`
	}
	if err := json.Unmarshal(rec.bodies[1], &patch); err != nil {
		t.Fatalf("decode patch body: %v", err)
	}
	if !strings.Contains(patch.Text, "carrying a parcel") {
		t.Errorf("patch text = %q, want the AI description", patch.Text)
	}
	if len(patch.Tags) == 0 {
		t.Errorf("patch tags = %v, want the annotation's tags preserved", patch.Tags)
	}
}

func TestGrafanaAnnotationsSilentFallsBackWhenPatchRejected(t *testing.T) {
	rec := &grafanaRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.methods = append(rec.methods, r.Method)
		rec.paths = append(rec.paths, r.URL.Path)
		rec.bodies = append(rec.bodies, body)
		// The annotation was deleted between the alert and its description.
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Annotation not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":5}`))
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{
		"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk", "tags": "",
	}

	if err := g.Send(nil, cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	// A rejected patch must not surface as a delivery failure — the
	// description still has to reach Grafana.
	if err := g.Send(nil, cfg, silentNotification()); err != nil {
		t.Fatalf("update Send after a rejected patch: %v", err)
	}

	if len(rec.methods) != 3 {
		t.Fatalf("server saw %d requests, want 3 (post, failed patch, fresh post)", len(rec.methods))
	}
	if rec.methods[2] != http.MethodPost {
		t.Errorf("third request method = %s, want a fresh POST", rec.methods[2])
	}
}

func TestGrafanaAnnotationsLoudRepeatCreatesNew(t *testing.T) {
	rec, srv := newGrafanaRecorder(t, `{"id":9}`)

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{
		"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk", "tags": "",
	}

	// Detection tags repeat across events, so a second loud alert is a new
	// detection and must not be folded into the previous annotation.
	for i := 0; i < 2; i++ {
		if err := g.Send(nil, cfg, loudNotification()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	if len(rec.methods) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(rec.methods))
	}
	for i, m := range rec.methods {
		if m != http.MethodPost {
			t.Errorf("request %d method = %s, want POST", i, m)
		}
	}
}

// --- alertmanager ---------------------------------------------------------

// grafanaEventIDOf pulls the event_id label out of a recorded Alertmanager
// payload.
func grafanaEventIDOf(t *testing.T, body []byte) string {
	t.Helper()

	var alerts []struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(body, &alerts); err != nil {
		t.Fatalf("decode alertmanager payload: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("payload carried %d alerts, want 1", len(alerts))
	}
	return alerts[0].Labels["event_id"]
}

func TestGrafanaAlertmanagerSilentReusesEventID(t *testing.T) {
	rec, srv := newGrafanaRecorder(t, `{}`)

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{
		"mode": grafanaModeAlertmanager, "url": srv.URL,
		"alertname": grafanaDefaultAlertname, "ttl": "900",
	}

	if err := g.Send(nil, cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if err := g.Send(nil, cfg, silentNotification()); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(rec.bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(rec.bodies))
	}

	first := grafanaEventIDOf(t, rec.bodies[0])
	second := grafanaEventIDOf(t, rec.bodies[1])
	if first == "" {
		t.Fatalf("first alert carried no event_id")
	}
	// Alertmanager identifies an alert by its labels, so an identical
	// event_id updates that alert rather than firing a second one.
	if first != second {
		t.Errorf("event_id = %q on the update, want %q (the id the alert was filed under)", second, first)
	}
}

func TestGrafanaAlertmanagerLoudRepeatGetsNewEventID(t *testing.T) {
	rec, srv := newGrafanaRecorder(t, `{}`)

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{
		"mode": grafanaModeAlertmanager, "url": srv.URL,
		"alertname": grafanaDefaultAlertname, "ttl": "900",
	}

	for i := 0; i < 2; i++ {
		if err := g.Send(nil, cfg, loudNotification()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	// Two detections on one camera must stay two alerts; collapsing them
	// would hide the second.
	if a, b := grafanaEventIDOf(t, rec.bodies[0]), grafanaEventIDOf(t, rec.bodies[1]); a == b {
		t.Errorf("both loud alerts used event_id %q, want distinct ids", a)
	}
}

// --- IRM ------------------------------------------------------------------

// grafanaIRMIDsOf pulls the alert_uid and the single alert's fingerprint out
// of a recorded IRM payload.
func grafanaIRMIDsOf(t *testing.T, body []byte) (alertUID, fingerprint string) {
	t.Helper()

	var payload struct {
		AlertUID string `json:"alert_uid"`
		Alerts   []struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode irm payload: %v", err)
	}
	if len(payload.Alerts) != 1 {
		t.Fatalf("payload carried %d alerts, want 1", len(payload.Alerts))
	}
	return payload.AlertUID, payload.Alerts[0].Fingerprint
}

func TestGrafanaIRMSilentReusesAlertUID(t *testing.T) {
	rec, srv := newGrafanaRecorder(t, `{}`)

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{"mode": grafanaModeIRM, "url": srv.URL}

	if err := g.Send(nil, cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if err := g.Send(nil, cfg, silentNotification()); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(rec.bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(rec.bodies))
	}

	firstUID, _ := grafanaIRMIDsOf(t, rec.bodies[0])
	secondUID, _ := grafanaIRMIDsOf(t, rec.bodies[1])
	if firstUID == "" {
		t.Fatalf("first alert carried no alert_uid")
	}
	// IRM keys a group on alert_uid, so reusing it revises that group.
	if firstUID != secondUID {
		t.Errorf("alert_uid = %q on the update, want %q (the id the group was opened under)", secondUID, firstUID)
	}
}

func TestGrafanaIRMFingerprintMatchesAlertUID(t *testing.T) {
	rec, srv := newGrafanaRecorder(t, `{}`)

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{"mode": grafanaModeIRM, "url": srv.URL}

	// With no publisher-supplied eventId, grafanaEventID returns fresh random
	// bytes per call — the two fields must still agree within one payload.
	if err := g.Send(nil, cfg, sdk.Notification{Title: "Motion detected"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	alertUID, fingerprint := grafanaIRMIDsOf(t, rec.bodies[0])
	if alertUID == "" || fingerprint == "" {
		t.Fatalf("alert_uid = %q, fingerprint = %q, want both populated", alertUID, fingerprint)
	}
	if alertUID != fingerprint {
		t.Errorf("alert_uid = %q but fingerprint = %q, want one identity for the alert", alertUID, fingerprint)
	}
}

// --- shared -------------------------------------------------------------

func TestGrafanaPublisherEventIDSurvives(t *testing.T) {
	rec, srv := newGrafanaRecorder(t, `{}`)

	g := newGrafana()
	g.client = srv.Client()
	cfg := map[string]string{
		"mode": grafanaModeAlertmanager, "url": srv.URL,
		"alertname": grafanaDefaultAlertname, "ttl": "900",
	}

	// When the publisher supplies its own eventId, that id is authoritative
	// and both publishes share it without the collapse store involved.
	notif := loudNotification()
	notif.Data = map[string]string{"eventId": "evt-123"}
	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if got := grafanaEventIDOf(t, rec.bodies[0]); got != "evt-123" {
		t.Errorf("event_id = %q, want the publisher's %q", got, "evt-123")
	}
}

func TestGrafanaCollapseTargetChangesWithConfig(t *testing.T) {
	base := map[string]string{"mode": grafanaModeIRM, "url": "https://a.example"}
	same := map[string]string{"url": "https://a.example", "mode": grafanaModeIRM}
	other := map[string]string{"mode": grafanaModeIRM, "url": "https://b.example"}

	// Map iteration order must not affect the key...
	if grafanaCollapseTarget(base) != grafanaCollapseTarget(same) {
		t.Errorf("target differs for equal configs: %q vs %q",
			grafanaCollapseTarget(base), grafanaCollapseTarget(same))
	}
	// ...but repointing at a different Grafana must, so records belonging to
	// the old target are never updated on the new one.
	if grafanaCollapseTarget(base) == grafanaCollapseTarget(other) {
		t.Errorf("target %q is shared by two different URLs", grafanaCollapseTarget(base))
	}
}
