package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

func TestGrafanaIDAndLabel(t *testing.T) {
	g := newGrafana()
	if g.ID() != "grafana" {
		t.Errorf("ID() = %q, want %q", g.ID(), "grafana")
	}
	if g.Label() != "Grafana" {
		t.Errorf("Label() = %q, want %q", g.Label(), "Grafana")
	}
}

func TestGrafanaIsRegistered(t *testing.T) {
	b, ok := Get("grafana")
	if !ok {
		t.Fatalf("Get(\"grafana\") ok = false, want true (init() must Register)")
	}
	if b.Label() != "Grafana" {
		t.Errorf("registered Label() = %q, want %q", b.Label(), "Grafana")
	}
}

func TestGrafanaSchemaAllFieldsGatedOnService(t *testing.T) {
	for _, f := range newGrafana().Schema() {
		if len(f.Condition) == 0 {
			t.Errorf("field %q: no Condition, want gated on service==grafana", f.Key)
			continue
		}
		if f.Condition[0].Key != "service" || f.Condition[0].Value != "grafana" {
			t.Errorf("field %q: Condition[0] = %+v, want service==grafana", f.Key, f.Condition[0])
		}
		if !strings.HasPrefix(f.Key, "grafana_") {
			t.Errorf("field key %q: want a grafana_ prefix so it cannot collide with another backend", f.Key)
		}
	}
}

func TestGrafanaSchemaModeSelector(t *testing.T) {
	byKey := map[string]sdk.JsonSchema{}
	for _, f := range newGrafana().Schema() {
		byKey[f.Key] = f
	}

	mode, ok := byKey["grafana_mode"]
	if !ok {
		t.Fatalf("schema missing %q field", "grafana_mode")
	}
	want := []string{grafanaModeAnnotations, grafanaModeAlerts, grafanaModeIRM}
	if !reflect.DeepEqual(mode.Enum, want) {
		t.Errorf("grafana_mode Enum = %v, want %v", mode.Enum, want)
	}
	if mode.DefaultValue != grafanaModeAnnotations {
		t.Errorf("grafana_mode DefaultValue = %v, want %q", mode.DefaultValue, grafanaModeAnnotations)
	}
	if !mode.Required {
		t.Errorf("grafana_mode Required = false, want true")
	}
}

func TestGrafanaSchemaSharedFieldsGatedOnModeIn(t *testing.T) {
	byKey := map[string]sdk.JsonSchema{}
	for _, f := range newGrafana().Schema() {
		byKey[f.Key] = f
	}

	for _, key := range []string{"grafana_server", "grafana_token"} {
		f, ok := byKey[key]
		if !ok {
			t.Fatalf("schema missing %q field", key)
		}
		if !f.Required {
			t.Errorf("%s Required = false, want true", key)
		}
		if len(f.Condition) != 2 {
			t.Fatalf("%s Condition = %+v, want 2 conditions (service + mode in)", key, f.Condition)
		}
		c := f.Condition[1]
		if c.Key != "grafana_mode" {
			t.Errorf("%s Condition[1].Key = %q, want %q", key, c.Key, "grafana_mode")
		}
		if c.Operator != sdk.SchemaConditionIn {
			t.Errorf("%s Condition[1].Operator = %q, want %q", key, c.Operator, sdk.SchemaConditionIn)
		}
		want := []string{grafanaModeAnnotations, grafanaModeAlerts}
		if !reflect.DeepEqual(c.Value, want) {
			t.Errorf("%s Condition[1].Value = %v, want %v", key, c.Value, want)
		}
	}

	if byKey["grafana_token"].Format != sdk.StringFormatPassword {
		t.Errorf("grafana_token Format = %q, want %q", byKey["grafana_token"].Format, sdk.StringFormatPassword)
	}
}

func TestGrafanaModeCondition(t *testing.T) {
	got := grafanaModeCondition(grafanaModeAlerts)
	want := []sdk.SchemaCondition{
		{Key: "service", Value: "grafana"},
		{Key: "grafana_mode", Value: grafanaModeAlerts},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("grafanaModeCondition = %+v, want %+v", got, want)
	}
}

func TestGrafanaParseTargetUnknownMode(t *testing.T) {
	g := newGrafana()
	if _, err := g.ParseTarget(map[string]any{"grafana_mode": "nope"}); err == nil {
		t.Fatalf("ParseTarget with unknown mode: got nil error, want error")
	}
}

func TestGrafanaSendUnknownMode(t *testing.T) {
	g := newGrafana()
	err := g.Send(context.Background(), map[string]string{"mode": "nope"}, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatalf("Send with unknown mode: got nil error, want error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("error = %q, want it to name the unknown mode", err.Error())
	}
}

func TestGrafanaServerAndToken(t *testing.T) {
	if _, _, err := grafanaServerAndToken(map[string]any{"grafana_token": "tk"}); err == nil {
		t.Errorf("missing server: got nil error, want error")
	}
	if _, _, err := grafanaServerAndToken(map[string]any{"grafana_server": "https://g.example"}); err == nil {
		t.Errorf("missing token: got nil error, want error")
	}

	server, token, err := grafanaServerAndToken(map[string]any{
		"grafana_server": "  https://g.example/  ",
		"grafana_token":  "  glsa_secret  ",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server != "https://g.example" {
		t.Errorf("server = %q, want trimmed with no trailing slash", server)
	}
	if token != "glsa_secret" {
		t.Errorf("token = %q, want %q", token, "glsa_secret")
	}
}

func TestGrafanaEventIDPrefersDataEventID(t *testing.T) {
	got := grafanaEventID(sdk.Notification{Data: map[string]string{"eventId": "evt-42"}})
	if got != "evt-42" {
		t.Errorf("grafanaEventID = %q, want %q", got, "evt-42")
	}
}

func TestGrafanaEventIDFallsBackToUniqueValue(t *testing.T) {
	a := grafanaEventID(sdk.Notification{Title: "x"})
	b := grafanaEventID(sdk.Notification{Title: "x"})
	if a == "" || b == "" {
		t.Fatalf("grafanaEventID returned empty: a=%q b=%q", a, b)
	}
	if a == b {
		t.Errorf("grafanaEventID returned %q twice, want a distinct id per event", a)
	}
}

func TestGrafanaSeverityDefaultsToInfo(t *testing.T) {
	if got := grafanaSeverity(sdk.Notification{}); got != string(sdk.SeverityInfo) {
		t.Errorf("grafanaSeverity(empty) = %q, want %q", got, sdk.SeverityInfo)
	}
	if got := grafanaSeverity(sdk.Notification{Severity: sdk.SeverityCritical}); got != string(sdk.SeverityCritical) {
		t.Errorf("grafanaSeverity(critical) = %q, want %q", got, sdk.SeverityCritical)
	}
}

func TestGrafanaAbsoluteDeepLink(t *testing.T) {
	if got := grafanaAbsoluteDeepLink(sdk.Notification{DeepLink: "/cameras/cam-1"}); got != "" {
		t.Errorf("relative deep link = %q, want \"\"", got)
	}
	abs := "https://cam.example/cameras/cam-1"
	if got := grafanaAbsoluteDeepLink(sdk.Notification{DeepLink: abs}); got != abs {
		t.Errorf("absolute deep link = %q, want %q", got, abs)
	}
}

func TestGrafanaCameraID(t *testing.T) {
	if got := grafanaCameraID(sdk.Notification{}); got != "" {
		t.Errorf("grafanaCameraID(no data) = %q, want \"\"", got)
	}
	got := grafanaCameraID(sdk.Notification{Data: map[string]string{"cameraId": " driveway "}})
	if got != "driveway" {
		t.Errorf("grafanaCameraID = %q, want %q", got, "driveway")
	}
}

func TestGrafanaPostJSONSendsHeadersAndBody(t *testing.T) {
	var gotMethod, gotContentType, gotAuth string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := grafanaPostJSON(context.Background(), srv.Client(), srv.URL,
		map[string]string{"Authorization": "Bearer tk"},
		map[string]string{"hello": "world"}, "grafana: test")
	if err != nil {
		t.Fatalf("grafanaPostJSON: unexpected error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotAuth != "Bearer tk" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tk")
	}
	if gotBody["hello"] != "world" {
		t.Errorf("body = %v, want hello=world", gotBody)
	}
}

func TestGrafanaPostJSONNon2xxReturnsStatusAndBodyButNotURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid token"))
	}))
	defer srv.Close()

	secretURL := srv.URL + "/integrations/v1/webhook/sup3rs3cr3t/"
	err := grafanaPostJSON(context.Background(), srv.Client(), secretURL, nil,
		map[string]string{"a": "b"}, "grafana: irm")
	if err == nil {
		t.Fatalf("got nil error, want error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q, want it to mention status 401", err.Error())
	}
	if !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("error = %q, want it to include the response body", err.Error())
	}
	if strings.Contains(err.Error(), "sup3rs3cr3t") {
		t.Errorf("error = %q, want it to NOT contain the request URL secret", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "grafana: irm: ") {
		t.Errorf("error = %q, want the %q prefix", err.Error(), "grafana: irm: ")
	}
}

func TestGrafanaPostJSONTransportErrorRedactsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := srv.Client()
	srv.Close() // force a connection-refused transport error

	secretURL := srv.URL + "/integrations/v1/webhook/sup3rs3cr3t/"
	err := grafanaPostJSON(context.Background(), client, secretURL, nil,
		map[string]string{"a": "b"}, "grafana: irm")
	if err == nil {
		t.Fatalf("got nil error, want a transport error")
	}
	if strings.Contains(err.Error(), "sup3rs3cr3t") {
		t.Errorf("error = %q, want the URL redacted out", err.Error())
	}
}
