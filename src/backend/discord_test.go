package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

func TestDiscordParseTargetMissingWebhook(t *testing.T) {
	d := &discord{}

	if _, err := d.ParseTarget(map[string]any{}); err == nil {
		t.Fatalf("ParseTarget with no webhook: got nil error, want error")
	}
	if _, err := d.ParseTarget(map[string]any{"discord_webhook": ""}); err == nil {
		t.Fatalf("ParseTarget with empty webhook: got nil error, want error")
	}
}

func TestDiscordParseTargetOK(t *testing.T) {
	d := &discord{}

	cfg, err := d.ParseTarget(map[string]any{"discord_webhook": "https://discord.com/api/webhooks/1/abc"})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["webhook"] != "https://discord.com/api/webhooks/1/abc" {
		t.Errorf("webhook = %q, want %q", cfg["webhook"], "https://discord.com/api/webhooks/1/abc")
	}
}

type decodedDiscordEmbed struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Color       int    `json:"color"`
	URL         string `json:"url"`
	Image       *struct {
		URL string `json:"url"`
	} `json:"image"`
}

type decodedDiscordPayload struct {
	Embeds []decodedDiscordEmbed `json:"embeds"`
}

func TestDiscordSendBasic(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody decodedDiscordPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := newDiscord()
	d.client = srv.Client()

	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}
	notif := sdk.Notification{Title: "Motion detected", Body: "Front door camera", Severity: sdk.SeverityInfo}

	if err := d.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/webhooks/1/abc" {
		t.Errorf("path = %q, want /api/webhooks/1/abc", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if len(gotBody.Embeds) != 1 {
		t.Fatalf("embeds = %+v, want exactly one", gotBody.Embeds)
	}
	embed := gotBody.Embeds[0]
	if embed.Title != "Motion detected" {
		t.Errorf("title = %q, want %q", embed.Title, "Motion detected")
	}
	if embed.Description != "Front door camera" {
		t.Errorf("description = %q, want %q", embed.Description, "Front door camera")
	}
	if embed.URL != "" {
		t.Errorf("url = %q, want empty when DeepLink unset", embed.URL)
	}
	if embed.Image != nil {
		t.Errorf("image = %+v, want nil when Thumbnail unset", embed.Image)
	}
}

func TestDiscordSendColorBySeverity(t *testing.T) {
	colorFor := func(sev sdk.Severity) int {
		var gotBody decodedDiscordPayload
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		d := newDiscord()
		d.client = srv.Client()
		cfg := map[string]string{"webhook": srv.URL}
		_ = d.Send(nil, cfg, sdk.Notification{Title: "x", Severity: sev})
		return gotBody.Embeds[0].Color
	}

	if got := colorFor(sdk.SeverityInfo); got != 0x3498db {
		t.Errorf("Info color = %#x, want %#x", got, 0x3498db)
	}
	if got := colorFor(sdk.SeverityWarn); got != 0xf1c40f {
		t.Errorf("Warn color = %#x, want %#x", got, 0xf1c40f)
	}
	if got := colorFor(sdk.SeverityError); got != 0xe74c3c {
		t.Errorf("Error color = %#x, want %#x", got, 0xe74c3c)
	}
	if got := colorFor(sdk.SeverityCritical); got != 0xe74c3c {
		t.Errorf("Critical color = %#x, want %#x", got, 0xe74c3c)
	}
}

func TestDiscordSendLinkOnlyWhenDeepLinkAbsolute(t *testing.T) {
	urlFor := func(deepLink string) string {
		var gotBody decodedDiscordPayload
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		d := newDiscord()
		d.client = srv.Client()
		cfg := map[string]string{"webhook": srv.URL}
		_ = d.Send(nil, cfg, sdk.Notification{Title: "x", DeepLink: deepLink})
		return gotBody.Embeds[0].URL
	}

	if got := urlFor("https://camera.example.com/cameras/cam-1"); got != "https://camera.example.com/cameras/cam-1" {
		t.Errorf("url = %q, want the absolute deep link", got)
	}
	if got := urlFor("/cameras/cam-1"); got != "" {
		t.Errorf("url = %q, want empty for a relative DeepLink", got)
	}
}

func TestDiscordSendWithThumbnailUsesMultipartPayloadJSONAndFile(t *testing.T) {
	var gotContentType string
	var gotPayload decodedDiscordPayload
	var gotFileBytes []byte
	var gotFileContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		_, params, err := mime.ParseMediaType(gotContentType)
		if err != nil {
			t.Fatalf("parse content-type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			b, _ := io.ReadAll(part)
			switch part.FormName() {
			case "payload_json":
				if err := json.Unmarshal(b, &gotPayload); err != nil {
					t.Fatalf("decode payload_json: %v", err)
				}
			case "files[0]":
				gotFileBytes = b
				gotFileContentType = part.Header.Get("Content-Type")
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := newDiscord()
	d.client = srv.Client()

	thumb := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	cfg := map[string]string{"webhook": srv.URL}
	notif := sdk.Notification{Title: "Motion detected", Body: "Front door", Thumbnail: thumb}

	if err := d.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", gotContentType)
	}
	if len(gotPayload.Embeds) != 1 {
		t.Fatalf("embeds = %+v, want exactly one", gotPayload.Embeds)
	}
	embed := gotPayload.Embeds[0]
	if embed.Image == nil || embed.Image.URL != "attachment://snapshot.jpg" {
		t.Errorf("image = %+v, want url attachment://snapshot.jpg", embed.Image)
	}
	if !bytes.Equal(gotFileBytes, thumb) {
		t.Errorf("files[0] bytes = %v, want %v", gotFileBytes, thumb)
	}
	if gotFileContentType != "image/jpeg" {
		t.Errorf("files[0] Content-Type = %q, want %q", gotFileContentType, "image/jpeg")
	}
}

func TestDiscordSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	d := newDiscord()
	d.client = srv.Client()

	cfg := map[string]string{"webhook": srv.URL}
	notif := sdk.Notification{Title: "x"}

	err := d.Send(nil, cfg, notif)
	if err == nil {
		t.Fatalf("Send: got nil error, want error on non-2xx response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
}

func TestDiscordIDAndLabel(t *testing.T) {
	d := &discord{}
	if d.ID() != "discord" {
		t.Errorf("ID() = %q, want %q", d.ID(), "discord")
	}
	if d.Label() != "Discord" {
		t.Errorf("Label() = %q, want %q", d.Label(), "Discord")
	}
}

func TestDiscordSchemaConditions(t *testing.T) {
	d := &discord{}
	schema := d.Schema()

	byKey := map[string]sdk.JsonSchema{}
	for _, f := range schema {
		byKey[f.Key] = f
		if len(f.Condition) != 1 || f.Condition[0].Key != "service" || f.Condition[0].Value != "discord" {
			t.Errorf("field %q: Condition = %+v, want gated on service==discord", f.Key, f.Condition)
		}
	}

	webhook, ok := byKey["discord_webhook"]
	if !ok {
		t.Fatalf("schema missing %q field", "discord_webhook")
	}
	if !webhook.Required || webhook.Format != sdk.StringFormatPassword {
		t.Errorf("webhook = %+v, want Required=true Format=password", webhook)
	}
}
