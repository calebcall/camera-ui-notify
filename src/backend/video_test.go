package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

// This file covers Notification.VideoURL — the "Video in Push" clip camera.ui
// 2.1.6 attaches to a detection when the camera (or the episode) has it
// enabled. No backend here can play a video inside a push the way the
// first-party app does, so the contract every one of them keeps is the same:
// surface the clip as a link, and never at the snapshot's expense.

// clipURL is deliberately awkward — a signed query string with the commas,
// semicolons and parens that ntfy's shorthand action format and Discord's
// markdown links would each mangle if the URL were pasted in naively.
const clipURL = "https://cam.example/api/recordings/evt-42/clip.mp4?exp=1754956800&sig=a,b;c(d)"

// videoNotification is a detection alert carrying both a snapshot URL and a
// clip, which is what camera.ui publishes for a video-enabled camera.
func videoNotification() sdk.Notification {
	return sdk.Notification{
		Title:    "Person detected",
		Body:     "Someone is at the door",
		Severity: sdk.SeverityWarn,
		ImageURL: "https://cam.example/snap.jpg",
		VideoURL: clipURL,
		DeepLink: "https://cam.example/cameras/driveway",
		Data:     map[string]string{"cameraId": "driveway", "eventId": "evt-42"},
	}
}

func TestVideoLinkRequiresAbsoluteURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absolute https", clipURL, clipURL},
		{"absolute http", "http://cam.local/clip.mp4", "http://cam.local/clip.mp4"},
		{"empty", "", ""},
		// SendNotification absolutizes a leading-slash clip path when
		// base_url is configured; when it isn't, no external service could
		// fetch this, so it must not become a dead link.
		{"router-relative", "/api/recordings/evt-42/clip.mp4", ""},
		{"scheme-less host", "cam.example/clip.mp4", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VideoLink(sdk.Notification{VideoURL: tc.in}); got != tc.want {
				t.Errorf("VideoLink(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNtfySendPublishesClipAsViewAction(t *testing.T) {
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()
	cfg := map[string]string{"server": srv.URL, "topic": "cams"}

	if err := n.Send(context.Background(), cfg, videoNotification()); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	var actions []ntfyAction
	if err := json.Unmarshal([]byte(gotHeaders.Get("Actions")), &actions); err != nil {
		t.Fatalf("decode Actions header %q: %v", gotHeaders.Get("Actions"), err)
	}
	if len(actions) != 1 {
		t.Fatalf("Actions has %d entries, want 1", len(actions))
	}
	if actions[0].Action != "view" || actions[0].Label != VideoLinkLabel {
		t.Errorf("action = %+v, want a %q action labelled %q", actions[0], "view", VideoLinkLabel)
	}
	// The whole URL must survive: the shorthand action format would have
	// split it at the first comma.
	if actions[0].URL != clipURL {
		t.Errorf("action url = %q, want %q", actions[0].URL, clipURL)
	}

	// The snapshot keeps the one attachment slot ntfy allows.
	if got := gotHeaders.Get("Attach"); got != "https://cam.example/snap.jpg" {
		t.Errorf("Attach = %q, want the snapshot URL — the clip must not displace it", got)
	}
}

func TestNtfySendOmitsActionsWithoutClip(t *testing.T) {
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newNtfy()
	n.client = srv.Client()

	notif := videoNotification()
	notif.VideoURL = ""
	if err := n.Send(context.Background(), map[string]string{"server": srv.URL, "topic": "cams"}, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if _, ok := gotHeaders["Actions"]; ok {
		t.Errorf("Actions = %q, want the header absent when there is no clip", gotHeaders.Get("Actions"))
	}
}

func TestGotifySendAppendsClipToMessage(t *testing.T) {
	var got struct {
		Message string `json:"message"`
		Extras  struct {
			ClientNotification struct {
				BigImageURL string `json:"bigImageUrl"`
			} `json:"client::notification"`
		} `json:"extras"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	g := newGotify()
	g.client = srv.Client()

	if err := g.Send(nil, map[string]string{"server": srv.URL, "token": "tok"}, videoNotification()); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if !strings.HasPrefix(got.Message, "Someone is at the door") {
		t.Errorf("message = %q, want it to still start with the body", got.Message)
	}
	if !strings.Contains(got.Message, VideoLinkLabel+": "+clipURL) {
		t.Errorf("message = %q, want it to carry the clip URL", got.Message)
	}
	if got.Extras.ClientNotification.BigImageURL != "https://cam.example/snap.jpg" {
		t.Errorf("bigImageUrl = %q, want the snapshot kept alongside the clip",
			got.Extras.ClientNotification.BigImageURL)
	}
}

func TestDiscordSendLinksClipInEmbedDescription(t *testing.T) {
	var got decodedDiscordPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := newDiscord()
	d.client = srv.Client()

	notif := videoNotification()
	if err := d.Send(nil, map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if len(got.Embeds) != 1 {
		t.Fatalf("posted %d embeds, want 1", len(got.Embeds))
	}
	desc := got.Embeds[0].Description
	if !strings.HasPrefix(desc, "Someone is at the door") {
		t.Errorf("description = %q, want the body first", desc)
	}
	// <> around the target so the paren inside the signature doesn't end the
	// markdown link early.
	if want := "[" + VideoLinkLabel + "](<" + clipURL + ">)"; !strings.Contains(desc, want) {
		t.Errorf("description = %q, want it to contain %q", desc, want)
	}
	// The embed's own url stays the deep link.
	if got.Embeds[0].URL != notif.DeepLink {
		t.Errorf("embed url = %q, want the deep link %q", got.Embeds[0].URL, notif.DeepLink)
	}
}

func TestDiscordEmbedClipWithoutBody(t *testing.T) {
	notif := videoNotification()
	notif.Body = ""

	embed := discordEmbedFor(notif)
	if want := "[" + VideoLinkLabel + "](<" + clipURL + ">)"; embed.Description != want {
		t.Errorf("description = %q, want exactly %q with no leading blank lines", embed.Description, want)
	}
}

func TestTelegramSendAddsClipButton(t *testing.T) {
	var got decodedTelegramMessagePayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL

	if err := tg.Send(nil, map[string]string{"token": "tk", "chat": "123"}, videoNotification()); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if got.ReplyMarkup == nil {
		t.Fatalf("reply_markup is nil, want the deep-link and clip buttons")
	}
	rows := got.ReplyMarkup.InlineKeyboard
	if len(rows) != 2 {
		t.Fatalf("inline_keyboard has %d rows, want 2 (deep link, then clip)", len(rows))
	}
	if rows[0][0].Text != DeepLinkLabelCamera {
		t.Errorf("first button = %q, want the deep link %q", rows[0][0].Text, DeepLinkLabelCamera)
	}
	if rows[1][0].Text != VideoLinkLabel || rows[1][0].URL != clipURL {
		t.Errorf("second button = %+v, want %q -> %q", rows[1][0], VideoLinkLabel, clipURL)
	}
}

// A clip with no deep link still gets a button: the markup is built from
// whichever links the notification actually carries, not from the deep link
// alone.
func TestTelegramReplyMarkupClipOnly(t *testing.T) {
	notif := videoNotification()
	notif.DeepLink = ""

	markup := telegramReplyMarkupFor(notif)
	if markup == nil {
		t.Fatalf("reply markup is nil, want a clip button")
	}
	if len(markup.InlineKeyboard) != 1 {
		t.Fatalf("inline_keyboard has %d rows, want 1", len(markup.InlineKeyboard))
	}
	if markup.InlineKeyboard[0][0].URL != clipURL {
		t.Errorf("button url = %q, want %q", markup.InlineKeyboard[0][0].URL, clipURL)
	}

	notif.VideoURL = ""
	if markup := telegramReplyMarkupFor(notif); markup != nil {
		t.Errorf("reply markup = %+v, want nil when nothing is linkable", markup)
	}
}

func TestPushoverSendClipFallsBackToMessage(t *testing.T) {
	var gotValues url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotValues, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newPushover()
	p.client = srv.Client()
	p.baseURL = srv.URL + "/1/messages.json"

	notif := videoNotification()
	if err := p.Send(nil, map[string]string{"token": "tk", "user": "u1"}, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	// The deep link holds the single url slot, so the clip goes in the text.
	if gotValues.Get("url") != notif.DeepLink {
		t.Errorf("url = %q, want the deep link %q", gotValues.Get("url"), notif.DeepLink)
	}
	if gotValues.Get("url_title") != DeepLinkLabelCamera {
		t.Errorf("url_title = %q, want %q", gotValues.Get("url_title"), DeepLinkLabelCamera)
	}
	if !strings.Contains(gotValues.Get("message"), VideoLinkLabel+": "+clipURL) {
		t.Errorf("message = %q, want it to carry the clip URL", gotValues.Get("message"))
	}
}

func TestPushoverSendClipClaimsFreeURLSlot(t *testing.T) {
	var gotValues url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotValues, _ = url.ParseQuery(string(b))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newPushover()
	p.client = srv.Client()
	p.baseURL = srv.URL + "/1/messages.json"

	notif := videoNotification()
	notif.DeepLink = ""
	if err := p.Send(nil, map[string]string{"token": "tk", "user": "u1"}, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if gotValues.Get("url") != clipURL {
		t.Errorf("url = %q, want the clip %q when no deep link claims the slot", gotValues.Get("url"), clipURL)
	}
	if gotValues.Get("url_title") != VideoLinkLabel {
		t.Errorf("url_title = %q, want %q", gotValues.Get("url_title"), VideoLinkLabel)
	}
	// Surfaced once, not twice.
	if strings.Contains(gotValues.Get("message"), clipURL) {
		t.Errorf("message = %q, want the clip left out once it has the url slot", gotValues.Get("message"))
	}
}

func TestWebhookSendForwardsVideoURL(t *testing.T) {
	var got struct {
		VideoURL string `json:"videoUrl"`
		ImageURL string `json:"imageUrl"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := newWebhook()
	w.client = srv.Client()

	notif := videoNotification()
	// A relative clip path reaches the webhook verbatim: the receiver is a
	// machine that may well be able to resolve it, unlike a phone.
	notif.VideoURL = "/api/recordings/evt-42/clip.mp4"
	if err := w.Send(nil, map[string]string{"url": srv.URL, "method": http.MethodPost}, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}

	if got.VideoURL != "/api/recordings/evt-42/clip.mp4" {
		t.Errorf("videoUrl = %q, want it forwarded verbatim", got.VideoURL)
	}
	if got.ImageURL != "https://cam.example/snap.jpg" {
		t.Errorf("imageUrl = %q, want the snapshot kept", got.ImageURL)
	}
}

func TestWebhookOmitsVideoURLWhenAbsent(t *testing.T) {
	var raw map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	w := newWebhook()
	w.client = srv.Client()

	notif := videoNotification()
	notif.VideoURL = ""
	if err := w.Send(nil, map[string]string{"url": srv.URL, "method": http.MethodPost}, notif); err != nil {
		t.Fatalf("Send: unexpected error: %v", err)
	}
	if _, ok := raw["videoUrl"]; ok {
		t.Errorf("videoUrl present (%v), want the key omitted when there is no clip", raw["videoUrl"])
	}
}

func TestGrafanaAlertmanagerSendAnnotatesVideoURL(t *testing.T) {
	got, _, _ := sendOneAlert(t, nil, videoNotification())

	if got.Annotations["video_url"] != clipURL {
		t.Errorf("annotations.video_url = %q, want %q", got.Annotations["video_url"], clipURL)
	}
	if got.Annotations["image_url"] != "https://cam.example/snap.jpg" {
		t.Errorf("annotations.image_url = %q, want the snapshot kept", got.Annotations["image_url"])
	}
}

func TestGrafanaIRMSendAnnotatesVideoURL(t *testing.T) {
	got := sendOneIRM(t, videoNotification())

	if got.CommonAnnotations["video_url"] != clipURL {
		t.Errorf("commonAnnotations.video_url = %q, want %q", got.CommonAnnotations["video_url"], clipURL)
	}
	if len(got.Alerts) != 1 {
		t.Fatalf("posted %d alerts, want 1", len(got.Alerts))
	}
	if got.Alerts[0].Annotations["video_url"] != clipURL {
		t.Errorf("alerts[0].annotations.video_url = %q, want %q",
			got.Alerts[0].Annotations["video_url"], clipURL)
	}
}

func TestGrafanaAnnotationTextLinksClip(t *testing.T) {
	text := grafanaAnnotationText(videoNotification())

	// html.EscapeString turns the query string's & into &amp;.
	want := `<a href="` + strings.ReplaceAll(clipURL, "&", "&amp;") + `">` + VideoLinkLabel + `</a>`
	if !strings.Contains(text, want) {
		t.Errorf("annotation text = %q, want it to contain %q", text, want)
	}
	if !strings.Contains(text, DeepLinkLabelCamera) {
		t.Errorf("annotation text = %q, want the deep-link anchor kept alongside the clip", text)
	}
}

// --- Clip upload (opt-in) ---------------------------------------------------
//
// Telegram and Discord can put the clip's bytes in the message itself. The
// plugin fetches those bytes (see src/notifier_test.go) and hands them to
// SendWithClip; these cover what each backend does with them, and the opt-in
// gate that decides whether the plugin fetches at all.

// fakeClip stands in for the MP4. Its contents only have to survive the
// multipart round trip intact.
var fakeClip = []byte("\x00\x00\x00\x18ftypmp42 not really an mp4, but bytes are bytes")

func TestTelegramClipLimitRequiresOptIn(t *testing.T) {
	tg := newTelegram()
	notif := videoNotification()

	if got := tg.ClipLimit(map[string]string{"token": "tk", "chat": "1"}, notif); got != 0 {
		t.Errorf("ClipLimit without opt-in = %d, want 0", got)
	}
	cfg := map[string]string{"token": "tk", "chat": "1", "clip": "true"}
	if got := tg.ClipLimit(cfg, notif); got != telegramMaxClipBytes {
		t.Errorf("ClipLimit with opt-in = %d, want %d", got, telegramMaxClipBytes)
	}
}

func TestTelegramParseTargetClipOptIn(t *testing.T) {
	tg := &telegram{}
	base := map[string]any{"telegram_token": "tk", "telegram_chat": "1"}

	cfg, err := tg.ParseTarget(base)
	if err != nil {
		t.Fatalf("ParseTarget: %v", err)
	}
	if _, ok := cfg["clip"]; ok {
		t.Errorf("clip present in cfg (%q), want absent when the toggle was never set", cfg["clip"])
	}

	// The config form declares a boolean, so a saved value arrives as a bool;
	// an unset one arrives as the "" configuredDevice passes for every field.
	for _, tc := range []struct {
		name string
		in   any
		want bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string false", "false", false},
		{"unset", "", false},
		{"nonsense", "yes please", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"telegram_token": "tk", "telegram_chat": "1", "telegram_clip": tc.in}
			cfg, err := tg.ParseTarget(input)
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			if got := cfg["clip"] == clipOptIn; got != tc.want {
				t.Errorf("clip opt-in = %v, want %v", got, tc.want)
			}
		})
	}
}

// A silent follow-up edits the caption of the message already carrying the
// clip, so the plugin must not be told to re-download it.
func TestTelegramClipLimitSkipsPendingEdit(t *testing.T) {
	tg := newTelegram()
	cfg := map[string]string{"token": "tk", "chat": "1", "clip": "true"}

	notif := videoNotification()
	notif.Tag = "motion:cam-1"
	notif.Silent = true

	// Nothing delivered yet: the follow-up will post a fresh message, which
	// does need the clip.
	if got := tg.ClipLimit(cfg, notif); got != telegramMaxClipBytes {
		t.Errorf("ClipLimit with no pending message = %d, want the full limit", got)
	}

	tg.collapse.remember(collapseKey("telegram", "tk", "1", "motion:cam-1"), "77", true)
	if got := tg.ClipLimit(cfg, notif); got != 0 {
		t.Errorf("ClipLimit for an editable follow-up = %d, want 0 (the clip is already in the chat)", got)
	}

	// The loud publish that opens the next event still uploads.
	notif.Silent = false
	if got := tg.ClipLimit(cfg, notif); got != telegramMaxClipBytes {
		t.Errorf("ClipLimit for a loud publish = %d, want the full limit", got)
	}
}

func TestTelegramSendWithClipPostsSendVideo(t *testing.T) {
	var gotPath string
	var gotFields map[string]string
	var gotFile struct {
		field, filename, contentType string
		data                         []byte
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotFields = map[string]string{}

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("parse content type: %v", err)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(part)
			if part.FileName() != "" {
				gotFile.field = part.FormName()
				gotFile.filename = part.FileName()
				gotFile.contentType = part.Header.Get("Content-Type")
				gotFile.data = data
				continue
			}
			gotFields[part.FormName()] = string(data)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL

	notif := videoNotification()
	notif.Thumbnail = []byte("jpeg-bytes")
	cfg := map[string]string{"token": "tk", "chat": "123", "clip": "true"}

	if err := tg.SendWithClip(nil, cfg, notif, fakeClip); err != nil {
		t.Fatalf("SendWithClip: unexpected error: %v", err)
	}

	if gotPath != "/bottk/sendVideo" {
		t.Errorf("path = %q, want /bottk/sendVideo", gotPath)
	}
	if gotFile.field != "video" || gotFile.filename != "clip.mp4" || gotFile.contentType != "video/mp4" {
		t.Errorf("file part = %+v, want a video/clip.mp4/video-mp4 part", gotFile)
	}
	if !bytes.Equal(gotFile.data, fakeClip) {
		t.Errorf("uploaded %d bytes, want the %d clip bytes intact", len(gotFile.data), len(fakeClip))
	}
	if gotFields["chat_id"] != "123" {
		t.Errorf("chat_id = %q, want %q", gotFields["chat_id"], "123")
	}
	if gotFields["caption"] != telegramText(notif) {
		t.Errorf("caption = %q, want the title/body text", gotFields["caption"])
	}
	if gotFields["supports_streaming"] != "true" {
		t.Errorf("supports_streaming = %q, want %q — the clip should play while it downloads",
			gotFields["supports_streaming"], "true")
	}

	// The "Play clip" button would point at a video already in the message.
	var markup telegramReplyMarkup
	if err := json.Unmarshal([]byte(gotFields["reply_markup"]), &markup); err != nil {
		t.Fatalf("decode reply_markup %q: %v", gotFields["reply_markup"], err)
	}
	if len(markup.InlineKeyboard) != 1 {
		t.Fatalf("inline_keyboard has %d rows, want only the deep-link row", len(markup.InlineKeyboard))
	}
	if markup.InlineKeyboard[0][0].Text != DeepLinkLabelCamera {
		t.Errorf("button = %q, want the deep link", markup.InlineKeyboard[0][0].Text)
	}
}

// A video message is captioned media, so the follow-up must edit it as a
// caption — editMessageText is rejected for any message carrying media.
func TestTelegramSendWithClipRecordsMediaForEdit(t *testing.T) {
	var gotMethods []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethods = append(gotMethods, strings.TrimPrefix(r.URL.Path, "/bottk/"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
	}))
	defer srv.Close()

	tg := newTelegram()
	tg.client = srv.Client()
	tg.baseURL = srv.URL
	cfg := map[string]string{"token": "tk", "chat": "123", "clip": "true"}

	notif := videoNotification()
	notif.Tag = "motion:cam-1"
	if err := tg.SendWithClip(nil, cfg, notif, fakeClip); err != nil {
		t.Fatalf("SendWithClip: unexpected error: %v", err)
	}

	notif.Silent = true
	notif.Body = "A person is walking up the driveway."
	if err := tg.Send(nil, cfg, notif); err != nil {
		t.Fatalf("follow-up Send: unexpected error: %v", err)
	}

	want := []string{"sendVideo", "editMessageCaption"}
	if len(gotMethods) != 2 || gotMethods[0] != want[0] || gotMethods[1] != want[1] {
		t.Errorf("methods = %v, want %v", gotMethods, want)
	}
}

func TestDiscordClipLimitRequiresOptIn(t *testing.T) {
	d := newDiscord()
	notif := videoNotification()

	if got := d.ClipLimit(map[string]string{"webhook": "https://x"}, notif); got != 0 {
		t.Errorf("ClipLimit without opt-in = %d, want 0", got)
	}
	cfg := map[string]string{"webhook": "https://x", "clip": "true"}
	if got := d.ClipLimit(cfg, notif); got != discordMaxClipBytes {
		t.Errorf("ClipLimit with opt-in = %d, want %d", got, discordMaxClipBytes)
	}
	// Discord re-uploads on an edit, so a silent follow-up still needs bytes.
	notif.Tag, notif.Silent = "motion:cam-1", true
	if got := d.ClipLimit(cfg, notif); got != discordMaxClipBytes {
		t.Errorf("ClipLimit for a follow-up = %d, want the full limit — Discord's edit re-sends files", got)
	}
}

// discordParts reads a multipart Discord request into its payload_json and
// its files[N] parts, in order.
type discordPart struct {
	field       string
	filename    string
	contentType string
	data        []byte
}

func readDiscordMultipart(t *testing.T, r *http.Request) (decodedDiscordEditPayload, []discordPart) {
	t.Helper()

	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("parse content type: %v", err)
	}
	var payload decodedDiscordEditPayload
	var files []discordPart

	mr := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		data, _ := io.ReadAll(part)
		if part.FormName() == "payload_json" {
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("decode payload_json: %v", err)
			}
			continue
		}
		files = append(files, discordPart{
			field:       part.FormName(),
			filename:    part.FileName(),
			contentType: part.Header.Get("Content-Type"),
			data:        data,
		})
	}
	return payload, files
}

type decodedDiscordEditPayload struct {
	Embeds      []decodedDiscordEmbed `json:"embeds"`
	Attachments *[]struct {
		ID       int    `json:"id"`
		Filename string `json:"filename"`
	} `json:"attachments"`
}

func TestDiscordSendWithClipAttachesBothFiles(t *testing.T) {
	var payload decodedDiscordEditPayload
	var files []discordPart

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, files = readDiscordMultipart(t, r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"555"}`))
	}))
	defer srv.Close()

	d := newDiscord()
	d.client = srv.Client()

	notif := videoNotification()
	notif.Thumbnail = []byte("jpeg-bytes")
	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc", "clip": "true"}

	if err := d.SendWithClip(nil, cfg, notif, fakeClip); err != nil {
		t.Fatalf("SendWithClip: unexpected error: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("posted %d files, want 2 (snapshot + clip)", len(files))
	}
	if files[0].field != "files[0]" || files[0].filename != "snapshot.jpg" {
		t.Errorf("files[0] = %+v, want the snapshot first so attachment://snapshot.jpg resolves", files[0])
	}
	if files[1].field != "files[1]" || files[1].filename != "clip.mp4" || files[1].contentType != "video/mp4" {
		t.Errorf("files[1] = %+v, want the clip as video/mp4", files[1])
	}
	if !bytes.Equal(files[1].data, fakeClip) {
		t.Errorf("uploaded %d clip bytes, want %d intact", len(files[1].data), len(fakeClip))
	}

	if len(payload.Embeds) != 1 {
		t.Fatalf("posted %d embeds, want 1", len(payload.Embeds))
	}
	// The snapshot still renders in the embed...
	if payload.Embeds[0].Image == nil || payload.Embeds[0].Image.URL != "attachment://snapshot.jpg" {
		t.Errorf("embed image = %+v, want the snapshot kept alongside the clip", payload.Embeds[0].Image)
	}
	// ...and the now-redundant link is gone.
	if strings.Contains(payload.Embeds[0].Description, VideoLinkLabel) {
		t.Errorf("description = %q, want no %q link when the clip is attached",
			payload.Embeds[0].Description, VideoLinkLabel)
	}
}

// Discord's edit keeps only the attachments the request names, so the
// follow-up has to re-send both files and list both ids.
func TestDiscordSendWithClipEditReUploadsBothFiles(t *testing.T) {
	var edit decodedDiscordEditPayload
	var editFiles []discordPart
	var methods []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPatch {
			edit, editFiles = readDiscordMultipart(t, r)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"555"}`))
	}))
	defer srv.Close()

	d := newDiscord()
	d.client = srv.Client()

	notif := videoNotification()
	notif.Thumbnail = []byte("jpeg-bytes")
	notif.Tag = "motion:cam-1"
	cfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc", "clip": "true"}

	if err := d.SendWithClip(nil, cfg, notif, fakeClip); err != nil {
		t.Fatalf("SendWithClip: unexpected error: %v", err)
	}
	notif.Silent = true
	notif.Body = "A person is walking up the driveway."
	if err := d.SendWithClip(nil, cfg, notif, fakeClip); err != nil {
		t.Fatalf("follow-up SendWithClip: unexpected error: %v", err)
	}

	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodPatch {
		t.Fatalf("methods = %v, want [POST PATCH]", methods)
	}
	if len(editFiles) != 2 {
		t.Fatalf("edit re-sent %d files, want 2 — Discord drops any it isn't given", len(editFiles))
	}
	if edit.Attachments == nil {
		t.Fatalf("edit sent no attachments array, which would drop both files")
	}
	got := *edit.Attachments
	if len(got) != 2 || got[0].ID != 0 || got[0].Filename != "snapshot.jpg" ||
		got[1].ID != 1 || got[1].Filename != "clip.mp4" {
		t.Errorf("edit attachments = %+v, want ids 0/1 naming snapshot.jpg and clip.mp4", got)
	}
}
