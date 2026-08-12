package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

// This file covers the opt-in clip-upload path: notifier.go fetching a
// notification's VideoURL and handing the bytes to a backend.ClipUploader
// (Telegram's sendVideo, a Discord file attachment). What the backends then
// do with those bytes lives in src/backend/video_test.go.
//
// The through-line of every case here is that the upload is best-effort:
// anything that goes wrong falls back to the plain Send, which delivers the
// clip as a link. A notification is never lost to a video.

// clipServer serves body at /clip.mp4 and reports how many times it was
// fetched, so a test can assert the clip was — or crucially wasn't —
// downloaded.
type clipServer struct {
	*httptest.Server
	fetches int
	// chunked suppresses Content-Length, so the size check has to happen
	// while reading the body rather than from the headers.
	chunked bool
	status  int
}

func newClipServer(t *testing.T, body []byte) *clipServer {
	t.Helper()
	cs := &clipServer{status: http.StatusOK}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.fetches++
		if cs.status != http.StatusOK {
			w.WriteHeader(cs.status)
			return
		}
		if !cs.chunked {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(cs.Close)
	return cs
}

// newClipPlugin wires a plugin whose one target is the fake backend, opted
// into clip upload at the given byte limit, and points the plugin's clip
// fetcher at the test server.
func newClipPlugin(t *testing.T, cs *clipServer, limit int) *NotifyPlugin {
	t.Helper()
	prev := clipFetchClient
	clipFetchClient = cs.Client()
	t.Cleanup(func() { clipFetchClient = prev })

	return newTestPlugin(map[string]any{
		"service":    "fake",
		"topic":      "sometopic",
		"clip_limit": strconv.Itoa(limit),
	})
}

func clipNotification(videoURL string) *sdk.Notification {
	return &sdk.Notification{
		Title:    "Person detected",
		Body:     "Someone is at the door",
		DeepLink: "https://cam.example/cameras/driveway",
		VideoURL: videoURL,
	}
}

func TestSendNotification_UploadsClipWhenBackendOptedIn(t *testing.T) {
	fb := registerFakeBackend()
	clip := bytes.Repeat([]byte("v"), 4096)
	cs := newClipServer(t, clip)
	p := newClipPlugin(t, cs, 1<<20)

	if err := p.SendNotification([]string{"cfg:fake"}, clipNotification(cs.URL+"/clip.mp4")); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}

	if got := fb.clipCallCount(); got != 1 {
		t.Fatalf("SendWithClip call count = %d, want 1", got)
	}
	if got := fb.callCount(); got != 0 {
		t.Fatalf("plain Send call count = %d, want 0 — the upload succeeded", got)
	}
	fb.mu.Lock()
	got := fb.clips[0]
	fb.mu.Unlock()
	if !bytes.Equal(got, clip) {
		t.Fatalf("uploaded %d bytes, want the %d fetched bytes intact", len(got), len(clip))
	}
}

// The default path must stay exactly as it was: no opt-in, no download.
func TestSendNotification_NoClipFetchWithoutOptIn(t *testing.T) {
	fb := registerFakeBackend()
	cs := newClipServer(t, []byte("clip"))

	prev := clipFetchClient
	clipFetchClient = cs.Client()
	t.Cleanup(func() { clipFetchClient = prev })

	p := newTestPlugin(map[string]any{"service": "fake", "topic": "sometopic"})
	if err := p.SendNotification([]string{"cfg:fake"}, clipNotification(cs.URL+"/clip.mp4")); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}

	if cs.fetches != 0 {
		t.Errorf("clip fetched %d times, want 0 when the target has not opted in", cs.fetches)
	}
	if got := fb.callCount(); got != 1 {
		t.Errorf("plain Send call count = %d, want 1", got)
	}
	if got := fb.clipCallCount(); got != 0 {
		t.Errorf("SendWithClip call count = %d, want 0", got)
	}
}

// An opted-in target with nothing to upload must not reach for the network.
func TestSendNotification_NoClipFetchWithoutVideoURL(t *testing.T) {
	fb := registerFakeBackend()
	cs := newClipServer(t, []byte("clip"))
	p := newClipPlugin(t, cs, 1<<20)

	n := clipNotification("")
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if cs.fetches != 0 {
		t.Errorf("clip fetched %d times, want 0 with no VideoURL", cs.fetches)
	}
	if got := fb.callCount(); got != 1 {
		t.Errorf("plain Send call count = %d, want 1", got)
	}
}

// A router-relative clip URL that base_url could not absolutize is not
// fetchable, so it must not be attempted.
func TestSendNotification_NoClipFetchForRelativeVideoURL(t *testing.T) {
	fb := registerFakeBackend()
	cs := newClipServer(t, []byte("clip"))
	p := newClipPlugin(t, cs, 1<<20)

	if err := p.SendNotification([]string{"cfg:fake"}, clipNotification("/api/recordings/evt-42/clip.mp4")); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if cs.fetches != 0 {
		t.Errorf("clip fetched %d times, want 0 for a relative URL", cs.fetches)
	}
	if got := fb.clipCallCount(); got != 0 {
		t.Errorf("SendWithClip call count = %d, want 0", got)
	}
}

func TestSendNotification_ClipFetchFailureFallsBackToLink(t *testing.T) {
	fb := registerFakeBackend()
	cs := newClipServer(t, []byte("clip"))
	cs.status = http.StatusNotFound
	p := newClipPlugin(t, cs, 1<<20)

	n := clipNotification(cs.URL + "/clip.mp4")
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v — a missing clip must not fail delivery", err)
	}
	if got := fb.clipCallCount(); got != 0 {
		t.Errorf("SendWithClip call count = %d, want 0", got)
	}
	if got := fb.callCount(); got != 1 {
		t.Fatalf("plain Send call count = %d, want 1 (the fallback)", got)
	}
	// The fallback still carries the URL, so the backend renders its link.
	fb.mu.Lock()
	delivered := fb.calls[0].n.VideoURL
	fb.mu.Unlock()
	if delivered != n.VideoURL {
		t.Errorf("fallback VideoURL = %q, want the clip URL kept for the link", delivered)
	}
}

// A backend that rejects the upload (too large for its real limit, an
// unplayable file) still delivers.
func TestSendNotification_ClipUploadRejectionFallsBackToLink(t *testing.T) {
	fb := registerFakeBackend()
	cs := newClipServer(t, []byte("clip-bytes"))
	p := newClipPlugin(t, cs, 1<<20)
	fb.failClip = true

	if err := p.SendNotification([]string{"cfg:fake"}, clipNotification(cs.URL+"/clip.mp4")); err != nil {
		t.Fatalf("SendNotification: unexpected error %v — a rejected upload must not fail delivery", err)
	}
	if got := fb.clipCallCount(); got != 1 {
		t.Errorf("SendWithClip call count = %d, want 1 (the attempt)", got)
	}
	if got := fb.callCount(); got != 1 {
		t.Errorf("plain Send call count = %d, want 1 (the fallback)", got)
	}
}

// Content-Length past the limit fails before the body is read, so an
// oversized clip costs headers rather than a multi-megabyte download.
func TestFetchClipRejectsOversizeByContentLength(t *testing.T) {
	body := bytes.Repeat([]byte("v"), 5000)
	cs := newClipServer(t, body)

	if _, err := fetchClip(t.Context(), cs.Client(), cs.URL+"/clip.mp4", 1000); err == nil {
		t.Fatalf("fetchClip: got nil error, want a size rejection")
	} else if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %v, want it to name the limit", err)
	}
}

// A chunked response has no Content-Length, so the overrun has to be caught
// while reading — and rejected, never truncated: a clipped MP4 is a broken
// upload, not a smaller video.
func TestFetchClipRejectsOversizeWhenChunked(t *testing.T) {
	body := bytes.Repeat([]byte("v"), 5000)
	cs := newClipServer(t, body)
	cs.chunked = true

	if _, err := fetchClip(t.Context(), cs.Client(), cs.URL+"/clip.mp4", 1000); err == nil {
		t.Fatalf("fetchClip: got nil error, want a size rejection")
	}
}

// A clip that exactly fills the limit is fine — the boundary is inclusive.
func TestFetchClipAcceptsExactlyTheLimit(t *testing.T) {
	body := bytes.Repeat([]byte("v"), 1000)
	cs := newClipServer(t, body)
	cs.chunked = true

	got, err := fetchClip(t.Context(), cs.Client(), cs.URL+"/clip.mp4", 1000)
	if err != nil {
		t.Fatalf("fetchClip: unexpected error %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("fetched %d bytes, want %d", len(got), len(body))
	}
}

func TestFetchClipRejectsEmptyBody(t *testing.T) {
	cs := newClipServer(t, nil)

	if _, err := fetchClip(t.Context(), cs.Client(), cs.URL+"/clip.mp4", 1000); err == nil {
		t.Fatalf("fetchClip: got nil error, want an error for an empty body")
	}
}

// The clip is fetched from the copy SendNotification absolutized, so a
// relative VideoURL plus base_url resolves to a real download.
func TestSendNotification_ClipFetchUsesBaseURLResolvedVideoURL(t *testing.T) {
	fb := registerFakeBackend()
	clip := []byte("clip-bytes")
	cs := newClipServer(t, clip)

	prev := clipFetchClient
	clipFetchClient = cs.Client()
	t.Cleanup(func() { clipFetchClient = prev })

	p := newTestPlugin(map[string]any{
		"service":    "fake",
		"topic":      "sometopic",
		"clip_limit": strconv.Itoa(1 << 20),
		"base_url":   cs.URL,
	})

	n := clipNotification("/clip.mp4")
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if cs.fetches != 1 {
		t.Fatalf("clip fetched %d times, want 1", cs.fetches)
	}
	if got := fb.clipCallCount(); got != 1 {
		t.Fatalf("SendWithClip call count = %d, want 1", got)
	}
	if n.VideoURL != "/clip.mp4" {
		t.Errorf("caller's *n.VideoURL mutated: got %q", n.VideoURL)
	}
}
