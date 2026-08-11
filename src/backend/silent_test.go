package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// This file covers how each backend handles the two fields camera.ui sets on
// a republish that supersedes an earlier notification: Tag (the collapse key)
// and Silent (this publish only updates, don't alert again). Backends that
// can edit a delivered message are exercised in telegram_test.go /
// discord_test.go; the ones that can't only have to deliver quietly.

// severityCritical is spelled out here because these tests flip severity on a
// notification they already built, and the critical case is the one carve-out
// in the Silent contract.
const severityCritical = sdk.SeverityCritical

// silentNotification is the AI-description republish: same tag as the alert
// it supersedes, flagged silent so it doesn't buzz the user a second time.
func silentNotification() sdk.Notification {
	return sdk.Notification{
		Title:    "Motion detected",
		Body:     "A person is walking up the driveway carrying a parcel.",
		Tag:      "motion:cam-1",
		Severity: sdk.SeverityInfo,
		Silent:   true,
	}
}

// loudNotification is the initial detection alert.
func loudNotification() sdk.Notification {
	return sdk.Notification{
		Title:    "Motion detected",
		Body:     "Front door",
		Tag:      "motion:cam-1",
		Severity: sdk.SeverityInfo,
	}
}

func TestNtfySendSilentUsesMinPriority(t *testing.T) {
	var gotPriority string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPriority = r.Header.Get("Priority")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &ntfy{client: srv.Client()}
	cfg := map[string]string{"server": srv.URL, "topic": "cams"}

	notif := silentNotification()
	if err := n.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPriority != "1" {
		t.Errorf("Priority = %q, want %q (ntfy's min: delivered without sound)", gotPriority, "1")
	}

	// A critical alert ignores Silent and keeps its severity priority.
	notif.Severity = severityCritical
	if err := n.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send critical: %v", err)
	}
	if gotPriority != "5" {
		t.Errorf("critical Priority = %q, want %q", gotPriority, "5")
	}
}

func TestGotifySendSilentUsesQuietPriority(t *testing.T) {
	var gotPriority int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Priority int `json:"priority"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		gotPriority = payload.Priority
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := &gotify{client: srv.Client()}
	cfg := map[string]string{"server": srv.URL, "token": "tok"}

	notif := silentNotification()
	if err := g.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Gotify treats 0-3 as in-app-only; 3 is the top of that band.
	if gotPriority != 3 {
		t.Errorf("priority = %d, want 3 (in-app list only, no system notification)", gotPriority)
	}

	notif.Severity = severityCritical
	if err := g.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send critical: %v", err)
	}
	if gotPriority != 10 {
		t.Errorf("critical priority = %d, want 10", gotPriority)
	}
}

func TestPushoverSendSilentUsesQuietPriority(t *testing.T) {
	var gotPriority string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotPriority = r.PostForm.Get("priority")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &pushover{client: srv.Client(), baseURL: srv.URL}
	cfg := map[string]string{"token": "tok", "user": "usr"}

	notif := silentNotification()
	if err := p.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotPriority != "-1" {
		t.Errorf("priority = %q, want %q (Pushover quiet: no sound or vibration)", gotPriority, "-1")
	}

	notif.Severity = severityCritical
	if err := p.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send critical: %v", err)
	}
	if gotPriority != "1" {
		t.Errorf("critical priority = %q, want %q", gotPriority, "1")
	}
}

func TestWebhookSendForwardsSilent(t *testing.T) {
	var payload struct {
		Tag    string `json:"tag"`
		Silent bool   `json:"silent"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Zeroed per request: `silent` is omitempty, so a stale true from the
		// previous decode would otherwise masquerade as this one's value.
		payload.Tag, payload.Silent = "", false
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &webhook{client: srv.Client(), now: func() time.Time { return time.Unix(0, 0) }}
	cfg := map[string]string{"url": srv.URL, "method": http.MethodPost}

	notif := silentNotification()
	if err := wh.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if payload.Tag != "motion:cam-1" {
		t.Errorf("tag = %q, want %q", payload.Tag, "motion:cam-1")
	}
	if !payload.Silent {
		t.Errorf("silent = false, want true — the receiver needs it to avoid re-alerting")
	}

	// The receiver is told to alert for a critical publish even if the host
	// flagged it silent, matching every other backend.
	notif.Severity = severityCritical
	if err := wh.Send(context.Background(), cfg, notif); err != nil {
		t.Fatalf("Send critical: %v", err)
	}
	if payload.Silent {
		t.Errorf("critical silent = true, want false")
	}
}

func TestWebhookSendOmitsSilentWhenLoud(t *testing.T) {
	var raw string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		raw = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := &webhook{client: srv.Client(), now: func() time.Time { return time.Unix(0, 0) }}
	cfg := map[string]string{"url": srv.URL, "method": http.MethodPost}

	if err := wh.Send(context.Background(), cfg, loudNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// omitempty keeps the payload byte-identical to pre-0.5.0 for the
	// ordinary case, so existing receivers see no change at all.
	if strings.Contains(raw, "silent") {
		t.Errorf("payload %q carries a silent key, want it omitted for a normal notification", raw)
	}
}
