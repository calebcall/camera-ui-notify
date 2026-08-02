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

type decodedGrafanaAnnotation struct {
	Time    int64    `json:"time"`
	TimeEnd *int64   `json:"timeEnd"`
	Tags    []string `json:"tags"`
	Text    string   `json:"text"`
}

func TestGrafanaAnnotationsParseRequiresServerAndToken(t *testing.T) {
	a := newGrafanaAnnotations()
	if _, err := a.parse(map[string]any{"grafana_token": "tk"}); err == nil {
		t.Errorf("missing server: got nil error, want error")
	}
	if _, err := a.parse(map[string]any{"grafana_server": "https://g.example"}); err == nil {
		t.Errorf("missing token: got nil error, want error")
	}
}

func TestGrafanaAnnotationsParseKeepsTags(t *testing.T) {
	a := newGrafanaAnnotations()
	cfg, err := a.parse(map[string]any{
		"grafana_server": "https://g.example/",
		"grafana_token":  "tk",
		"grafana_tags":   " home, security ",
	})
	if err != nil {
		t.Fatalf("parse: unexpected error: %v", err)
	}
	if cfg["server"] != "https://g.example" {
		t.Errorf("server = %q, want trailing slash trimmed", cfg["server"])
	}
	if cfg["token"] != "tk" {
		t.Errorf("token = %q, want %q", cfg["token"], "tk")
	}
	if cfg["tags"] != "home, security" {
		t.Errorf("tags = %q, want %q", cfg["tags"], "home, security")
	}
}

func TestGrafanaAnnotationsSchemaGating(t *testing.T) {
	fields := newGrafanaAnnotations().schema()
	if len(fields) != 1 {
		t.Fatalf("schema has %d fields, want 1", len(fields))
	}
	f := fields[0]
	if f.Key != "grafana_tags" {
		t.Errorf("key = %q, want %q", f.Key, "grafana_tags")
	}
	if f.Required {
		t.Errorf("grafana_tags Required = true, want false")
	}
	want := grafanaModeCondition(grafanaModeAnnotations)
	if !reflect.DeepEqual(f.Condition, want) {
		t.Errorf("Condition = %+v, want %+v", f.Condition, want)
	}
}

func TestGrafanaAnnotationsSendBasic(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody decodedGrafanaAnnotation

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

	cfg := map[string]string{
		"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk", "tags": "",
	}
	notif := sdk.Notification{
		Title:    "Person detected",
		Body:     "Someone is at the door",
		Severity: sdk.SeverityWarn,
		Data:     map[string]string{"cameraId": "driveway"},
	}

	before := time.Now().UnixMilli()
	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	after := time.Now().UnixMilli()

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/annotations" {
		t.Errorf("path = %q, want /api/annotations", gotPath)
	}
	if gotAuth != "Bearer tk" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tk")
	}
	if gotBody.Time < before || gotBody.Time > after {
		t.Errorf("time = %d, want a unix-ms value between %d and %d", gotBody.Time, before, after)
	}
	if gotBody.TimeEnd != nil {
		t.Errorf("timeEnd = %v, want absent (point-in-time annotation)", *gotBody.TimeEnd)
	}
	wantTags := []string{"camera.ui", "camera:driveway", "severity:warn"}
	if !reflect.DeepEqual(gotBody.Tags, wantTags) {
		t.Errorf("tags = %v, want %v", gotBody.Tags, wantTags)
	}
	if !strings.Contains(gotBody.Text, "<b>Person detected</b>") {
		t.Errorf("text = %q, want it to contain the bolded title", gotBody.Text)
	}
	if !strings.Contains(gotBody.Text, "Someone is at the door") {
		t.Errorf("text = %q, want it to contain the body", gotBody.Text)
	}
	if strings.Contains(gotBody.Text, "<a href") {
		t.Errorf("text = %q, want no anchor when the deep link is unset", gotBody.Text)
	}
}

func TestGrafanaAnnotationsSendTagsOmitCameraAndAppendExtras(t *testing.T) {
	var gotBody decodedGrafanaAnnotation

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{
		"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk",
		"tags": " home , , security ,",
	}
	notif := sdk.Notification{Title: "Plugin updated", Severity: sdk.SeverityInfo}

	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	want := []string{"camera.ui", "severity:info", "home", "security"}
	if !reflect.DeepEqual(gotBody.Tags, want) {
		t.Errorf("tags = %v, want %v (no camera tag, blank extras dropped)", gotBody.Tags, want)
	}
}

func TestGrafanaAnnotationsSendEscapesAndLinks(t *testing.T) {
	var gotBody decodedGrafanaAnnotation

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk"}
	notif := sdk.Notification{
		Title:    "Person <script>alert(1)</script>",
		Body:     "Front & back",
		DeepLink: "https://cam.example/cameras/driveway",
		Data:     map[string]string{"cameraId": "driveway"},
	}

	if err := g.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if strings.Contains(gotBody.Text, "<script>") {
		t.Errorf("text = %q, want the title's markup escaped", gotBody.Text)
	}
	if !strings.Contains(gotBody.Text, "Front &amp; back") {
		t.Errorf("text = %q, want the body HTML-escaped", gotBody.Text)
	}
	wantAnchor := `<a href="https://cam.example/cameras/driveway">` + DeepLinkLabelCamera + `</a>`
	if !strings.Contains(gotBody.Text, wantAnchor) {
		t.Errorf("text = %q, want it to contain %q", gotBody.Text, wantAnchor)
	}
}

func TestGrafanaAnnotationsSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("no annotation permission"))
	}))
	defer srv.Close()

	g := newGrafana()
	g.client = srv.Client()

	cfg := map[string]string{"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk"}
	err := g.Send(nil, cfg, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatalf("got nil error, want error on 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to mention status 403", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "grafana: annotations: ") {
		t.Errorf("error = %q, want the %q prefix", err.Error(), "grafana: annotations: ")
	}
}
