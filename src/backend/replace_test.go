package backend

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordedRequest is one request a replacement test's fake server saw.
type recordedRequest struct {
	method string
	path   string
	query  string
	// body is the JSON body, or the payload_json part of a multipart body.
	body []byte
	// files lists the multipart file parts by form name, empty for JSON.
	files []string
}

// decodeRequest reads a request into a recordedRequest, transparently
// unwrapping multipart/form-data so assertions can look at payload_json the
// same way they look at a plain JSON body.
func decodeRequest(t *testing.T, r *http.Request) recordedRequest {
	t.Helper()

	rec := recordedRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		rec.body, _ = io.ReadAll(r.Body)
		return rec
	}

	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(part)
		switch {
		case part.FormName() == "payload_json":
			rec.body = data
		case part.FileName() != "":
			rec.files = append(rec.files, part.FormName())
		default:
			// A plain text field (Telegram's chat_id, caption, ...); the
			// Telegram tests below assert on those through the raw body.
			rec.body = append(rec.body, []byte(part.FormName()+"="+string(data)+"\n")...)
		}
	}
	return rec
}

// --- Telegram -------------------------------------------------------------

func TestTelegramSendReplacesTaggedMessage(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, decodeRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":4242}}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), baseURL: srv.URL, collapse: newCollapseStore()}
	cfg := map[string]string{"token": "tok", "chat": "99"}

	if err := tg.Send(context.Background(), cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if err := tg.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	if !strings.HasSuffix(got[0].path, "/sendMessage") {
		t.Errorf("first request path = %q, want .../sendMessage", got[0].path)
	}
	// The whole point: the second publish edits the first message rather than
	// posting a duplicate.
	if !strings.HasSuffix(got[1].path, "/editMessageText") {
		t.Fatalf("second request path = %q, want .../editMessageText", got[1].path)
	}

	var edit struct {
		ChatID    string `json:"chat_id"`
		MessageID string `json:"message_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(got[1].body, &edit); err != nil {
		t.Fatalf("decode edit payload: %v", err)
	}
	if edit.MessageID != "4242" {
		t.Errorf("message_id = %q, want %q (the id the first send returned)", edit.MessageID, "4242")
	}
	if edit.ChatID != "99" {
		t.Errorf("chat_id = %q, want %q", edit.ChatID, "99")
	}
	if !strings.Contains(edit.Text, "carrying a parcel") {
		t.Errorf("edit text = %q, want the AI description", edit.Text)
	}
}

func TestTelegramSendEditsCaptionForPhotoMessage(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, decodeRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":7}}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), baseURL: srv.URL, collapse: newCollapseStore()}
	cfg := map[string]string{"token": "tok", "chat": "99"}

	withPhoto := loudNotification()
	withPhoto.Thumbnail = []byte{0xff, 0xd8, 0xff}
	if err := tg.Send(context.Background(), cfg, withPhoto); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if err := tg.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	if !strings.HasSuffix(got[0].path, "/sendPhoto") {
		t.Errorf("first request path = %q, want .../sendPhoto", got[0].path)
	}
	// Telegram rejects editMessageText against a photo message, so the
	// delivered kind decides the edit method.
	if !strings.HasSuffix(got[1].path, "/editMessageCaption") {
		t.Errorf("second request path = %q, want .../editMessageCaption", got[1].path)
	}
	var edit struct {
		Caption string `json:"caption"`
	}
	_ = json.Unmarshal(got[1].body, &edit)
	if !strings.Contains(edit.Caption, "carrying a parcel") {
		t.Errorf("caption = %q, want the AI description", edit.Caption)
	}
}

func TestTelegramSendSilentDisablesNotification(t *testing.T) {
	var payload struct {
		DisableNotification bool `json:"disable_notification"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload.DisableNotification = false
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	// No collapse store: a restart loses the message id, and the silent
	// update then has to arrive as a new-but-quiet message.
	tg := &telegram{client: srv.Client(), baseURL: srv.URL}
	cfg := map[string]string{"token": "tok", "chat": "99"}

	if err := tg.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !payload.DisableNotification {
		t.Errorf("disable_notification = false, want true")
	}

	if err := tg.Send(context.Background(), cfg, loudNotification()); err != nil {
		t.Fatalf("Send loud: %v", err)
	}
	if payload.DisableNotification {
		t.Errorf("disable_notification = true for a normal alert, want false")
	}
}

func TestTelegramSendFallsBackWhenEditRejected(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := decodeRequest(t, r)
		got = append(got, rec)
		// Telegram rejects the edit the way it does for a deleted message.
		if strings.Contains(rec.path, "/edit") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"message to edit not found"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":5}}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), baseURL: srv.URL, collapse: newCollapseStore()}
	cfg := map[string]string{"token": "tok", "chat": "99"}

	if err := tg.Send(context.Background(), cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	// A rejected edit must not surface as a delivery failure — the content
	// still has to reach the user.
	if err := tg.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("update Send after a rejected edit: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("server saw %d requests, want 3 (send, failed edit, fresh send)", len(got))
	}
	if !strings.HasSuffix(got[2].path, "/sendMessage") {
		t.Errorf("third request path = %q, want a fresh .../sendMessage", got[2].path)
	}
}

func TestTelegramSendWithoutTagNeverEdits(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, decodeRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":8}}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), baseURL: srv.URL, collapse: newCollapseStore()}
	cfg := map[string]string{"token": "tok", "chat": "99"}

	untagged := loudNotification()
	untagged.Tag = ""
	for i := 0; i < 2; i++ {
		if err := tg.Send(context.Background(), cfg, untagged); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	for i, rec := range got {
		if !strings.HasSuffix(rec.path, "/sendMessage") {
			t.Errorf("request %d path = %q, want .../sendMessage — untagged alerts are unrelated events", i, rec.path)
		}
	}
}

func TestTelegramSendDoesNotReplaceOnANewLoudEvent(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, decodeRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":11}}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), baseURL: srv.URL, collapse: newCollapseStore()}
	cfg := map[string]string{"token": "tok", "chat": "99"}

	// Detection tags repeat across events ("motion:cam-1"), so a second loud
	// alert under the same tag is a new event, not an update — rewriting the
	// first would erase it from the chat.
	for i := 0; i < 2; i++ {
		if err := tg.Send(context.Background(), cfg, loudNotification()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	for i, rec := range got {
		if !strings.HasSuffix(rec.path, "/sendMessage") {
			t.Errorf("request %d path = %q, want .../sendMessage", i, rec.path)
		}
	}
}

func TestTelegramSendUpdateFollowsTheLatestAlert(t *testing.T) {
	var got []recordedRequest
	ids := []string{"100", "200"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := decodeRequest(t, r)
		got = append(got, rec)
		w.Header().Set("Content-Type", "application/json")
		id := "0"
		if len(ids) > 0 {
			id, ids = ids[0], ids[1:]
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":` + id + `}}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), baseURL: srv.URL, collapse: newCollapseStore()}
	cfg := map[string]string{"token": "tok", "chat": "99"}

	// Two events, then the description for the second: the update must land
	// on the most recent alert, not the one it already superseded.
	for i := 0; i < 2; i++ {
		if err := tg.Send(context.Background(), cfg, loudNotification()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	if err := tg.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("server saw %d requests, want 3", len(got))
	}
	var edit struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(got[2].body, &edit); err != nil {
		t.Fatalf("decode edit payload: %v", err)
	}
	if edit.MessageID != "200" {
		t.Errorf("edited message_id = %q, want %q (the second alert)", edit.MessageID, "200")
	}
}

// --- Discord --------------------------------------------------------------

func TestDiscordSendReplacesTaggedMessage(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, decodeRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1234567890"}`))
	}))
	defer srv.Close()

	d := &discord{client: srv.Client(), collapse: newCollapseStore()}
	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}

	if err := d.Send(context.Background(), cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if err := d.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	// The id only comes back when the execute-webhook call asks for it.
	if got[0].method != http.MethodPost || got[0].query != "wait=true" {
		t.Errorf("first request = %s ?%s, want POST ?wait=true", got[0].method, got[0].query)
	}
	if got[1].method != http.MethodPatch {
		t.Fatalf("second request method = %s, want PATCH", got[1].method)
	}
	if want := "/api/webhooks/1/abc/messages/1234567890"; got[1].path != want {
		t.Errorf("edit path = %q, want %q", got[1].path, want)
	}

	var edit struct {
		Embeds []struct {
			Description string `json:"description"`
		} `json:"embeds"`
		Attachments *[]struct{} `json:"attachments"`
	}
	if err := json.Unmarshal(got[1].body, &edit); err != nil {
		t.Fatalf("decode edit payload: %v", err)
	}
	if len(edit.Embeds) != 1 || !strings.Contains(edit.Embeds[0].Description, "carrying a parcel") {
		t.Errorf("edit embeds = %+v, want the AI description", edit.Embeds)
	}
	// Omitting attachments on an edit deletes any image already on the
	// message, so an image-free edit must send an explicit empty array.
	if edit.Attachments == nil {
		t.Errorf("edit payload omits attachments, want an explicit empty array")
	}
}

func TestDiscordSendSilentSuppressesNotification(t *testing.T) {
	var payload struct {
		Flags int `json:"flags"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload.Flags = 0
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1"}`))
	}))
	defer srv.Close()

	// No collapse store: a restart loses the message id, and the silent
	// update then has to arrive as a new-but-quiet message.
	d := &discord{client: srv.Client()}
	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}

	if err := d.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if payload.Flags != discordSuppressNotifications {
		t.Errorf("flags = %d, want %d (SUPPRESS_NOTIFICATIONS)", payload.Flags, discordSuppressNotifications)
	}

	if err := d.Send(context.Background(), cfg, loudNotification()); err != nil {
		t.Fatalf("Send loud: %v", err)
	}
	if payload.Flags != 0 {
		t.Errorf("flags = %d for a normal alert, want 0", payload.Flags)
	}
}

func TestDiscordSendEditReUploadsThumbnail(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, decodeRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"55"}`))
	}))
	defer srv.Close()

	d := &discord{client: srv.Client(), collapse: newCollapseStore()}
	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}

	first := loudNotification()
	first.Thumbnail = []byte{0xff, 0xd8, 0xff}
	if err := d.Send(context.Background(), cfg, first); err != nil {
		t.Fatalf("initial Send: %v", err)
	}

	update := silentNotification()
	update.Thumbnail = []byte{0xff, 0xd8, 0xff}
	if err := d.Send(context.Background(), cfg, update); err != nil {
		t.Fatalf("update Send: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	if len(got[1].files) != 1 || got[1].files[0] != "files[0]" {
		t.Errorf("edit file parts = %v, want [files[0]]", got[1].files)
	}

	var edit struct {
		Attachments []struct {
			ID       int    `json:"id"`
			Filename string `json:"filename"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(got[1].body, &edit); err != nil {
		t.Fatalf("decode edit payload: %v", err)
	}
	// Discord matches the re-uploaded file to the attachment entry by id.
	if len(edit.Attachments) != 1 || edit.Attachments[0].Filename != "snapshot.jpg" {
		t.Errorf("edit attachments = %+v, want one snapshot.jpg entry", edit.Attachments)
	}
}

func TestDiscordSendFallsBackWhenEditRejected(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := decodeRequest(t, r)
		got = append(got, rec)
		if r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Unknown Message","code":10008}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"77"}`))
	}))
	defer srv.Close()

	d := &discord{client: srv.Client(), collapse: newCollapseStore()}
	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}

	if err := d.Send(context.Background(), cfg, loudNotification()); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	if err := d.Send(context.Background(), cfg, silentNotification()); err != nil {
		t.Fatalf("update Send after a rejected edit: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("server saw %d requests, want 3 (post, failed edit, fresh post)", len(got))
	}
	if got[2].method != http.MethodPost {
		t.Errorf("third request method = %s, want a fresh POST", got[2].method)
	}
}

func TestDiscordSendDoesNotReplaceOnANewLoudEvent(t *testing.T) {
	var got []recordedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, decodeRequest(t, r))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"321"}`))
	}))
	defer srv.Close()

	d := &discord{client: srv.Client(), collapse: newCollapseStore()}
	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}

	// See the Telegram equivalent: a repeated tag on a loud alert is a new
	// event, and must not overwrite the previous one in the channel.
	for i := 0; i < 2; i++ {
		if err := d.Send(context.Background(), cfg, loudNotification()); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	for i, rec := range got {
		if rec.method != http.MethodPost {
			t.Errorf("request %d method = %s, want POST", i, rec.method)
		}
	}
}

func TestDiscordMessageURLPreservesThreadID(t *testing.T) {
	got, err := discordMessageURL("https://discord.com/api/webhooks/1/abc?thread_id=42", "99")
	if err != nil {
		t.Fatalf("discordMessageURL: %v", err)
	}
	want := "https://discord.com/api/webhooks/1/abc/messages/99?thread_id=42"
	if got != want {
		t.Errorf("discordMessageURL = %q, want %q", got, want)
	}
}
