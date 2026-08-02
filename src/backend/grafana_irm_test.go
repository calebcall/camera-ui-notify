package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

type decodedGrafanaIRM struct {
	AlertUID string `json:"alert_uid"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	ImageURL string `json:"image_url"`
	Link     string `json:"link_to_upstream_details"`
	State    string `json:"state"`
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
