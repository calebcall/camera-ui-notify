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

func TestTelegramParseTargetMissingToken(t *testing.T) {
	tg := &telegram{}

	if _, err := tg.ParseTarget(map[string]any{"telegram_chat": "123"}); err == nil {
		t.Fatalf("ParseTarget with no token: got nil error, want error")
	}
	if _, err := tg.ParseTarget(map[string]any{"telegram_token": "", "telegram_chat": "123"}); err == nil {
		t.Fatalf("ParseTarget with empty token: got nil error, want error")
	}
}

func TestTelegramParseTargetMissingChat(t *testing.T) {
	tg := &telegram{}

	if _, err := tg.ParseTarget(map[string]any{"telegram_token": "tk"}); err == nil {
		t.Fatalf("ParseTarget with no chat: got nil error, want error")
	}
	if _, err := tg.ParseTarget(map[string]any{"telegram_token": "tk", "telegram_chat": ""}); err == nil {
		t.Fatalf("ParseTarget with empty chat: got nil error, want error")
	}
}

func TestTelegramParseTargetOK(t *testing.T) {
	tg := &telegram{}

	cfg, err := tg.ParseTarget(map[string]any{"telegram_token": "tk_secret", "telegram_chat": "123"})
	if err != nil {
		t.Fatalf("ParseTarget: unexpected error: %v", err)
	}
	if cfg["token"] != "tk_secret" {
		t.Errorf("token = %q, want %q", cfg["token"], "tk_secret")
	}
	if cfg["chat"] != "123" {
		t.Errorf("chat = %q, want %q", cfg["chat"], "123")
	}
}

type decodedTelegramMessagePayload struct {
	ChatID      string `json:"chat_id"`
	Text        string `json:"text"`
	ReplyMarkup *struct {
		InlineKeyboard [][]struct {
			Text string `json:"text"`
			URL  string `json:"url"`
		} `json:"inline_keyboard"`
	} `json:"reply_markup"`
}

func TestTelegramSendMessageBasic(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody decodedTelegramMessagePayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL

	cfg := map[string]string{"token": "tk_secret", "chat": "123"}
	notif := sdk.Notification{Title: "Motion detected", Body: "Front door camera"}

	if err := tg.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/bottk_secret/sendMessage" {
		t.Errorf("path = %q, want %q", gotPath, "/bottk_secret/sendMessage")
	}
	if gotBody.ChatID != "123" {
		t.Errorf("chat_id = %q, want %q", gotBody.ChatID, "123")
	}
	if gotBody.Text != "Motion detected\nFront door camera" {
		t.Errorf("text = %q, want %q", gotBody.Text, "Motion detected\nFront door camera")
	}
	if gotBody.ReplyMarkup != nil {
		t.Errorf("reply_markup = %+v, want nil when DeepLink unset", gotBody.ReplyMarkup)
	}
}

func TestTelegramSendMessageOmitsBodyNewlineWhenEmpty(t *testing.T) {
	var gotBody decodedTelegramMessagePayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL

	cfg := map[string]string{"token": "tk", "chat": "123"}
	notif := sdk.Notification{Title: "Motion detected"}

	if err := tg.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if gotBody.Text != "Motion detected" {
		t.Errorf("text = %q, want %q (no trailing newline)", gotBody.Text, "Motion detected")
	}
}

func TestTelegramSendMessageLinkOnlyWhenDeepLinkAbsolute(t *testing.T) {
	bodyFor := func(deepLink string) decodedTelegramMessagePayload {
		var gotBody decodedTelegramMessagePayload
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		tg := newTelegram()
		tg.client = srv.Client()
		tg.baseURL = srv.URL

		cfg := map[string]string{"token": "tk", "chat": "123"}
		_ = tg.Send(nil, cfg, sdk.Notification{Title: "x", DeepLink: deepLink})
		return gotBody
	}

	abs := bodyFor("https://camera.example.com/cameras/cam-1")
	if abs.ReplyMarkup == nil || len(abs.ReplyMarkup.InlineKeyboard) != 1 || len(abs.ReplyMarkup.InlineKeyboard[0]) != 1 {
		t.Fatalf("reply_markup = %+v, want one inline button", abs.ReplyMarkup)
	}
	btn := abs.ReplyMarkup.InlineKeyboard[0][0]
	if btn.URL != "https://camera.example.com/cameras/cam-1" || btn.Text != "Open camera" {
		t.Errorf("button = %+v, want url=%q text=%q", btn, "https://camera.example.com/cameras/cam-1", "Open camera")
	}

	rel := bodyFor("/cameras/cam-1")
	if rel.ReplyMarkup != nil {
		t.Errorf("reply_markup = %+v, want nil for a relative DeepLink", rel.ReplyMarkup)
	}
}

func TestTelegramSendPhotoWithThumbnail(t *testing.T) {
	var gotPath, gotContentType string
	var gotFields map[string]string
	var gotFileBytes []byte
	var gotFileContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		_, params, err := mime.ParseMediaType(gotContentType)
		if err != nil {
			t.Fatalf("parse content-type: %v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		gotFields = map[string]string{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("next part: %v", err)
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "photo" {
				gotFileBytes = b
				gotFileContentType = part.Header.Get("Content-Type")
			} else {
				gotFields[part.FormName()] = string(b)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL

	thumb := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	cfg := map[string]string{"token": "tk_secret", "chat": "123"}
	notif := sdk.Notification{
		Title:     "Motion detected",
		Body:      "Front door",
		Thumbnail: thumb,
		DeepLink:  "https://camera.example.com/cameras/cam-1",
	}

	if err := tg.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotPath != "/bottk_secret/sendPhoto" {
		t.Errorf("path = %q, want %q", gotPath, "/bottk_secret/sendPhoto")
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", gotContentType)
	}
	if gotFields["chat_id"] != "123" {
		t.Errorf("chat_id = %q, want %q", gotFields["chat_id"], "123")
	}
	if gotFields["caption"] != "Motion detected\nFront door" {
		t.Errorf("caption = %q, want %q", gotFields["caption"], "Motion detected\nFront door")
	}
	if !bytes.Equal(gotFileBytes, thumb) {
		t.Errorf("photo bytes = %v, want %v", gotFileBytes, thumb)
	}
	if gotFileContentType != "image/jpeg" {
		t.Errorf("photo Content-Type = %q, want %q", gotFileContentType, "image/jpeg")
	}
	if !strings.Contains(gotFields["reply_markup"], "https://camera.example.com/cameras/cam-1") {
		t.Errorf("reply_markup = %q, want it to contain the absolute deep link", gotFields["reply_markup"])
	}
}

func TestTelegramSendPhotoOmitsReplyMarkupWhenDeepLinkRelative(t *testing.T) {
	var gotFields map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		gotFields = map[string]string{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if part.FormName() != "photo" {
				b, _ := io.ReadAll(part)
				gotFields[part.FormName()] = string(b)
			} else {
				_, _ = io.ReadAll(part)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL

	cfg := map[string]string{"token": "tk", "chat": "123"}
	notif := sdk.Notification{Title: "x", Thumbnail: []byte{0x01}, DeepLink: "/cameras/cam-1"}

	if err := tg.Send(nil, cfg, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if _, ok := gotFields["reply_markup"]; ok {
		t.Errorf("reply_markup present, want absent for a relative DeepLink")
	}
}

func TestTelegramSendNon2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL

	cfg := map[string]string{"token": "tk", "chat": "123"}
	notif := sdk.Notification{Title: "x"}

	err := tg.Send(nil, cfg, notif)
	if err == nil {
		t.Fatalf("Send: got nil error, want error on non-2xx response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to mention status 500", err.Error())
	}
}

func TestTelegramIDAndLabel(t *testing.T) {
	tg := &telegram{}
	if tg.ID() != "telegram" {
		t.Errorf("ID() = %q, want %q", tg.ID(), "telegram")
	}
	if tg.Label() != "Telegram" {
		t.Errorf("Label() = %q, want %q", tg.Label(), "Telegram")
	}
}

func TestTelegramSchemaConditions(t *testing.T) {
	tg := &telegram{}
	schema := tg.Schema()

	byKey := map[string]sdk.JsonSchema{}
	for _, f := range schema {
		byKey[f.Key] = f
		if len(f.Condition) != 1 || f.Condition[0].Key != "service" || f.Condition[0].Value != "telegram" {
			t.Errorf("field %q: Condition = %+v, want gated on service==telegram", f.Key, f.Condition)
		}
	}

	token, ok := byKey["telegram_token"]
	if !ok {
		t.Fatalf("schema missing %q field", "telegram_token")
	}
	if !token.Required || token.Format != sdk.StringFormatPassword {
		t.Errorf("token = %+v, want Required=true Format=password", token)
	}

	chat, ok := byKey["telegram_chat"]
	if !ok {
		t.Fatalf("schema missing %q field", "telegram_chat")
	}
	if !chat.Required {
		t.Errorf("chat Required = false, want true")
	}
	if chat.Format == sdk.StringFormatPassword {
		t.Errorf("chat Format = password, want a plain field")
	}
}

// TestTelegramSendTransportErrorRedactsToken proves a transport failure does
// not leak the bot token (which is in the request URL path) into the error.
func TestTelegramSendTransportErrorRedactsToken(t *testing.T) {
	tg := newTelegram()
	tg.baseURL = "http://127.0.0.1:1" // unreachable local addr → connection refused
	cfg := map[string]string{"token": "123456:SECRETTOKEN", "chat": "42"}
	err := tg.Send(nil, cfg, sdk.Notification{Title: "x"})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("bot token leaked into error: %q", err.Error())
	}
}
