package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "github.com/cameraui/sdk/go"

	"github.com/calebcall/camera-ui-notify/src/backend"
)

// fakeSendCall records one invocation of fakeBackend.Send, for assertions
// about what notifier.go dispatched.
type fakeSendCall struct {
	cfg map[string]string
	n   sdk.Notification
}

// fakeBackend is a minimal backend.Backend used to exercise notifier.go's
// dispatch logic without depending on any real delivery backend. It
// self-registers once under id "fake" via registerFakeBackend, and its
// mutable test knobs (failTopics) are guarded by mu so tests can run
// sequentially against the single shared instance.
type fakeBackend struct {
	mu         sync.Mutex
	calls      []fakeSendCall
	failTopics map[string]bool

	// clipCalls records every SendWithClip invocation, and failClip makes
	// them fail, so tests can drive notifier.go's clip-upload path and its
	// fall back to the plain Send.
	clipCalls []fakeSendCall
	clips     [][]byte
	failClip  bool
}

func (f *fakeBackend) ID() string    { return "fake" }
func (f *fakeBackend) Label() string { return "Fake" }

func (f *fakeBackend) Schema() []sdk.JsonSchema {
	return []sdk.JsonSchema{
		{
			Type:      sdk.JsonSchemaTypeString,
			Key:       "topic",
			Title:     "Topic",
			Required:  true,
			Condition: []sdk.SchemaCondition{{Key: "service", Value: "fake"}},
		},
		// configuredDevice only forwards the fields a backend declares, so
		// the clip-upload knob has to appear here for ParseTarget to see it.
		{
			Type:      sdk.JsonSchemaTypeString,
			Key:       "clip_limit",
			Title:     "Clip limit",
			Condition: []sdk.SchemaCondition{{Key: "service", Value: "fake"}},
		},
	}
}

func (f *fakeBackend) ParseTarget(input map[string]any) (map[string]string, error) {
	topic, _ := input["topic"].(string)
	if topic == "" {
		return nil, fmt.Errorf("topic is required")
	}
	cfg := map[string]string{"topic": topic}
	// A test opts this backend into clip upload by storing the byte limit it
	// should report, which stands in for a real backend's "Upload video
	// clips" toggle plus its own size ceiling.
	if limit, _ := input["clip_limit"].(string); limit != "" {
		cfg["clip_limit"] = limit
	}
	return cfg, nil
}

// ClipLimit implements backend.ClipUploader.
func (f *fakeBackend) ClipLimit(cfg map[string]string, n sdk.Notification) int {
	limit, err := strconv.Atoi(cfg["clip_limit"])
	if err != nil {
		return 0
	}
	return limit
}

// SendWithClip implements backend.ClipUploader.
func (f *fakeBackend) SendWithClip(ctx context.Context, cfg map[string]string, n sdk.Notification, clip []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clipCalls = append(f.clipCalls, fakeSendCall{cfg: cfg, n: n})
	f.clips = append(f.clips, clip)
	if f.failClip {
		return fmt.Errorf("fake clip upload rejected")
	}
	return nil
}

func (f *fakeBackend) Send(ctx context.Context, cfg map[string]string, n sdk.Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeSendCall{cfg: cfg, n: n})
	if f.failTopics[cfg["topic"]] {
		return fmt.Errorf("fake send failure for topic %q", cfg["topic"])
	}
	return nil
}

func (f *fakeBackend) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.failTopics = map[string]bool{}
	f.clipCalls = nil
	f.clips = nil
	f.failClip = false
}

func (f *fakeBackend) clipCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clipCalls)
}

func (f *fakeBackend) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

var (
	sharedFakeBackend     *fakeBackend
	registerFakeBackendMu sync.Once
)

// registerFakeBackend registers the single shared fakeBackend instance with
// the package-level backend registry exactly once (backend.Register panics
// on a duplicate id, and every test in this file shares one process/test
// binary, including repeated iterations under `go test -count=2`), then
// resets its recorded state for the calling test.
func registerFakeBackend() *fakeBackend {
	registerFakeBackendMu.Do(func() {
		sharedFakeBackend = &fakeBackend{}
		backend.Register(sharedFakeBackend)
	})
	sharedFakeBackend.reset()
	return sharedFakeBackend
}

// newTestPlugin builds a NotifyPlugin backed by an in-memory
// *sdk.DeviceStorage seeded with values, so tests never touch a real
// persistence layer. DeviceStorage.Values is an exported field and
// GetValue reads directly from it when no schema is registered (schema is
// only consulted for OnGet/DefaultValue), so a bare struct literal is a
// faithful stand-in for the host-constructed storage.
func newTestPlugin(values map[string]any) *NotifyPlugin {
	if values == nil {
		values = map[string]any{}
	}
	return &NotifyPlugin{
		BasePlugin: sdk.NewBasePlugin(&sdk.Logger{}, nil, &sdk.DeviceStorage{Values: values}),
	}
}

func TestGetDevices_Unconfigured(t *testing.T) {
	p := newTestPlugin(nil)

	got, err := p.GetDevices([]string{"u1"})
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("GetDevices(unconfigured) = %d devices, want 0", len(got))
	}
}

func TestGetDevices_UnknownService(t *testing.T) {
	p := newTestPlugin(map[string]any{"service": "not-a-real-service"})

	got, err := p.GetDevices([]string{"u1"})
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("GetDevices(unknown service) = %d devices, want 0", len(got))
	}
}

func TestGetDevices_ConfiguredNtfy(t *testing.T) {
	p := newTestPlugin(map[string]any{
		"service":     "ntfy",
		"ntfy_server": "https://ntfy.example.com",
		"ntfy_topic":  "alerts",
	})

	got, err := p.GetDevices([]string{"u1", "u2"})
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("GetDevices(configured ntfy) = %d devices, want 1", len(got))
	}

	dev := got[0]
	if dev.ID != "cfg:ntfy" {
		t.Fatalf("ID = %q, want %q", dev.ID, "cfg:ntfy")
	}
	if dev.OwnerUserID != "u1" {
		t.Fatalf("OwnerUserID = %q, want ownerUserIDs[0] = %q", dev.OwnerUserID, "u1")
	}
	if !dev.Active {
		t.Fatalf("expected synthesized device to be Active")
	}
	if svc, ok := dev.Metadata["service"].(string); !ok || svc != "ntfy" {
		t.Fatalf("Metadata[service] = %#v, want string \"ntfy\"", dev.Metadata["service"])
	}
	if server, ok := dev.Metadata["server"].(string); !ok || server != "https://ntfy.example.com" {
		t.Fatalf("Metadata[server] = %#v, want string \"https://ntfy.example.com\"", dev.Metadata["server"])
	}
	if topic, ok := dev.Metadata["topic"].(string); !ok || topic != "alerts" {
		t.Fatalf("Metadata[topic] = %#v, want string \"alerts\"", dev.Metadata["topic"])
	}
}

func TestGetDevices_IncompleteConfigReturnsEmpty(t *testing.T) {
	// service is set but the required ntfy_topic field is missing, so
	// ntfy.ParseTarget errors — that must be swallowed as "not configured
	// yet", not surfaced as an error.
	p := newTestPlugin(map[string]any{"service": "ntfy"})

	got, err := p.GetDevices([]string{"u1"})
	if err != nil {
		t.Fatalf("GetDevices: unexpected error %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("GetDevices(incomplete config) = %d devices, want 0", len(got))
	}
}

func TestGetDevices_NoOwnerUserIDs(t *testing.T) {
	p := newTestPlugin(map[string]any{
		"service":    "ntfy",
		"ntfy_topic": "alerts",
	})

	got, err := p.GetDevices(nil)
	if err != nil {
		t.Fatalf("GetDevices: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("GetDevices(nil owners) = %d devices, want 1", len(got))
	}
	if got[0].OwnerUserID != "" {
		t.Fatalf("OwnerUserID = %q, want empty when ownerUserIDs is empty", got[0].OwnerUserID)
	}
}

func TestGetDevice(t *testing.T) {
	p := newTestPlugin(map[string]any{
		"service":    "ntfy",
		"ntfy_topic": "alerts",
	})

	dev, err := p.GetDevice("cfg:ntfy")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if dev == nil {
		t.Fatalf("GetDevice(cfg:ntfy) = nil, want the synthesized device")
	}
	if dev.ID != "cfg:ntfy" {
		t.Fatalf("ID = %q, want %q", dev.ID, "cfg:ntfy")
	}

	dev, err = p.GetDevice("other")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if dev != nil {
		t.Fatalf("GetDevice(other) = %+v, want nil", dev)
	}
}

func TestSendNotification_DispatchesToConfiguredBackend(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service": "fake",
		"topic":   "sometopic",
	})

	n := &sdk.Notification{Title: "hello", Body: "world"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1", got)
	}
}

func TestSendNotification_PartialFailureDoesNotAbort(t *testing.T) {
	fb := registerFakeBackend()
	fb.failTopics["fail-topic"] = true
	p := newTestPlugin(map[string]any{
		"service": "fake",
		"topic":   "fail-topic",
	})

	n := &sdk.Notification{Title: "hello", Body: "world"}
	err := p.SendNotification([]string{"cfg:fake", "does-not-exist"}, n)
	if err == nil {
		t.Fatalf("expected joined error from the failing device")
	}
	if !strings.Contains(err.Error(), "fail-topic") {
		t.Fatalf("error %v does not reference the failing device/topic", err)
	}
	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1 (the unknown id is skipped, not dispatched)", got)
	}
}

func TestSendNotification_BaseURLMakesRelativeDeepLinkAbsolute(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":  "fake",
		"topic":    "sometopic",
		"base_url": "https://camera.example.com",
	})

	n := &sdk.Notification{Title: "hello", DeepLink: "/cameras/cam-1?startTs=123"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1", got)
	}
	fb.mu.Lock()
	gotDeepLink := fb.calls[0].n.DeepLink
	fb.mu.Unlock()
	want := "https://camera.example.com/cameras/cam-1?startTs=123"
	if gotDeepLink != want {
		t.Fatalf("dispatched DeepLink = %q, want %q", gotDeepLink, want)
	}
	if n.DeepLink != "/cameras/cam-1?startTs=123" {
		t.Fatalf("caller's *n.DeepLink mutated: got %q, want unchanged relative path", n.DeepLink)
	}
}

func TestSendNotification_BaseURLTrimsTrailingSlash(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":  "fake",
		"topic":    "sometopic",
		"base_url": "https://camera.example.com/",
	})

	n := &sdk.Notification{Title: "hello", DeepLink: "/cameras/cam-1"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	fb.mu.Lock()
	gotDeepLink := fb.calls[0].n.DeepLink
	fb.mu.Unlock()
	want := "https://camera.example.com/cameras/cam-1"
	if gotDeepLink != want {
		t.Fatalf("dispatched DeepLink = %q, want %q", gotDeepLink, want)
	}
}

func TestSendNotification_NoBaseURLLeavesDeepLinkUnchanged(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service": "fake",
		"topic":   "sometopic",
	})

	n := &sdk.Notification{Title: "hello", DeepLink: "/cameras/cam-1"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	fb.mu.Lock()
	gotDeepLink := fb.calls[0].n.DeepLink
	fb.mu.Unlock()
	if gotDeepLink != "/cameras/cam-1" {
		t.Fatalf("dispatched DeepLink = %q, want unchanged %q", gotDeepLink, "/cameras/cam-1")
	}
}

func TestSendNotification_BaseURLDoesNotAffectAlreadyAbsoluteDeepLink(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":  "fake",
		"topic":    "sometopic",
		"base_url": "https://camera.example.com",
	})

	n := &sdk.Notification{Title: "hello", DeepLink: "https://other.example.com/x"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	fb.mu.Lock()
	gotDeepLink := fb.calls[0].n.DeepLink
	fb.mu.Unlock()
	if gotDeepLink != "https://other.example.com/x" {
		t.Fatalf("dispatched DeepLink = %q, want unchanged absolute URL", gotDeepLink)
	}
}

// camera.ui publishes the "Video in Push" clip URL absolute, but a publisher
// that hands over a server-relative clip path gets the same base_url
// treatment the deep link does — otherwise backend.VideoLink would drop it as
// unopenable and the clip would never reach the user.
func TestSendNotification_BaseURLMakesRelativeVideoURLAbsolute(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":  "fake",
		"topic":    "sometopic",
		"base_url": "https://camera.example.com/",
	})

	n := &sdk.Notification{
		Title:    "hello",
		DeepLink: "/cameras/cam-1",
		VideoURL: "/api/recordings/evt-42/clip.mp4",
	}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	fb.mu.Lock()
	got := fb.calls[0].n
	fb.mu.Unlock()

	want := "https://camera.example.com/api/recordings/evt-42/clip.mp4"
	if got.VideoURL != want {
		t.Fatalf("dispatched VideoURL = %q, want %q", got.VideoURL, want)
	}
	if got.DeepLink != "https://camera.example.com/cameras/cam-1" {
		t.Fatalf("dispatched DeepLink = %q, want it absolutized too", got.DeepLink)
	}
	if n.VideoURL != "/api/recordings/evt-42/clip.mp4" {
		t.Fatalf("caller's *n.VideoURL mutated: got %q, want unchanged relative path", n.VideoURL)
	}
}

func TestSendNotification_BaseURLLeavesAbsoluteVideoURLUnchanged(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":  "fake",
		"topic":    "sometopic",
		"base_url": "https://camera.example.com",
	})

	n := &sdk.Notification{Title: "hello", VideoURL: "https://clips.example.com/evt-42.mp4"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	fb.mu.Lock()
	gotVideo := fb.calls[0].n.VideoURL
	fb.mu.Unlock()
	if gotVideo != "https://clips.example.com/evt-42.mp4" {
		t.Fatalf("dispatched VideoURL = %q, want unchanged absolute URL", gotVideo)
	}
}

// The clip must never be mistaken for a snapshot: resolveThumbnail fetches
// ImageURL only, so a notification carrying a clip and no image still
// delivers with no inline bytes rather than an MP4 in the photo slot.
func TestSendNotification_VideoURLIsNotFetchedAsThumbnail(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service": "fake",
		"topic":   "sometopic",
	})

	n := &sdk.Notification{Title: "hello", VideoURL: "https://clips.example.com/evt-42.mp4"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	fb.mu.Lock()
	got := fb.calls[0].n
	fb.mu.Unlock()
	if len(got.Thumbnail) != 0 {
		t.Fatalf("dispatched Thumbnail = %d bytes, want none — the clip is not a snapshot", len(got.Thumbnail))
	}
	if got.VideoURL != "https://clips.example.com/evt-42.mp4" {
		t.Fatalf("dispatched VideoURL = %q, want it passed through", got.VideoURL)
	}
}

func TestSendNotification_SkipsUnknownID(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service": "fake",
		"topic":   "t",
	})

	n := &sdk.Notification{Title: "hello"}
	if err := p.SendNotification([]string{"does-not-exist"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 0 {
		t.Fatalf("Send call count = %d, want 0 for an unknown device id", got)
	}
}

// withImageFetchServer starts an httptest server with handler, points the
// package-level imageFetchClient at it, and returns the server plus a cleanup
// that restores the original client. Used by the ImageURL-resolution tests.
func withImageFetchServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	old := imageFetchClient
	imageFetchClient = srv.Client()
	t.Cleanup(func() {
		imageFetchClient = old
		srv.Close()
	})
	return srv
}

func TestSendNotification_FetchesImageURLIntoThumbnail(t *testing.T) {
	fb := registerFakeBackend()

	imgBytes := []byte{0xff, 0xd8, 0xff, 0xe0, 0x01, 0x02, 0x03}
	var gotPath string
	srv := withImageFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imgBytes)
	})

	p := newTestPlugin(map[string]any{"service": "fake", "topic": "t"})
	n := &sdk.Notification{Title: "hi", ImageURL: srv.URL + "/snap.jpg"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}

	if gotPath != "/snap.jpg" {
		t.Fatalf("fetched path = %q, want /snap.jpg", gotPath)
	}
	fb.mu.Lock()
	got := fb.calls[0].n
	fb.mu.Unlock()
	if !bytes.Equal(got.Thumbnail, imgBytes) {
		t.Fatalf("dispatched Thumbnail = %v, want fetched bytes %v", got.Thumbnail, imgBytes)
	}
	if len(n.Thumbnail) != 0 {
		t.Fatalf("caller's *n.Thumbnail mutated: got %d bytes, want 0", len(n.Thumbnail))
	}
}

func TestSendNotification_ExistingThumbnailNotOverwrittenByImageURL(t *testing.T) {
	fb := registerFakeBackend()

	var hits int
	srv := withImageFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("SHOULD-NOT-BE-FETCHED"))
	})

	inline := []byte("inline-bytes")
	p := newTestPlugin(map[string]any{"service": "fake", "topic": "t"})
	n := &sdk.Notification{Title: "hi", Thumbnail: inline, ImageURL: srv.URL + "/snap.jpg"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}

	if hits != 0 {
		t.Fatalf("fetched ImageURL %d times, want 0 when an inline Thumbnail is present", hits)
	}
	fb.mu.Lock()
	got := fb.calls[0].n
	fb.mu.Unlock()
	if !bytes.Equal(got.Thumbnail, inline) {
		t.Fatalf("Thumbnail = %q, want unchanged inline bytes %q", got.Thumbnail, inline)
	}
}

func TestSendNotification_ImageURLFetchFailureDegradesGracefully(t *testing.T) {
	fb := registerFakeBackend()

	srv := withImageFetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	})

	p := newTestPlugin(map[string]any{"service": "fake", "topic": "t"})
	n := &sdk.Notification{Title: "hi", ImageURL: srv.URL + "/snap.jpg"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification must not error on image-fetch failure: %v", err)
	}

	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1 (delivery proceeds despite fetch failure)", got)
	}
	fb.mu.Lock()
	got := fb.calls[0].n
	fb.mu.Unlock()
	if len(got.Thumbnail) != 0 {
		t.Fatalf("Thumbnail = %d bytes, want 0 after a failed fetch", len(got.Thumbnail))
	}
	if got.ImageURL != srv.URL+"/snap.jpg" {
		t.Fatalf("ImageURL = %q, want left intact so URL-capable backends still render it", got.ImageURL)
	}
}

func TestRegisterDevice_ReturnsError(t *testing.T) {
	p := newTestPlugin(nil)

	dev, err := p.RegisterDevice("u1", map[string]any{"service": "ntfy", "ntfy_topic": "t"})
	if err == nil {
		t.Fatalf("expected RegisterDevice to error (targets are config-driven, not registered)")
	}
	if dev != nil {
		t.Fatalf("RegisterDevice error path = %+v, want nil device", dev)
	}
}

func TestUpdateDevice_Benign(t *testing.T) {
	p := newTestPlugin(map[string]any{
		"service":    "ntfy",
		"ntfy_topic": "alerts",
	})

	dev, err := p.UpdateDevice("cfg:ntfy", map[string]any{"active": false})
	if err != nil {
		t.Fatalf("UpdateDevice: unexpected error %v", err)
	}
	if dev != nil {
		t.Fatalf("UpdateDevice = %+v, want nil (no-op)", dev)
	}
}

func TestRevokeDevice_Benign(t *testing.T) {
	p := newTestPlugin(map[string]any{
		"service":    "ntfy",
		"ntfy_topic": "alerts",
	})

	if err := p.RevokeDevice("cfg:ntfy"); err != nil {
		t.Fatalf("RevokeDevice: unexpected error %v", err)
	}

	// Revoking is a no-op: the device is still synthesized from config
	// afterwards.
	dev, err := p.GetDevice("cfg:ntfy")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if dev == nil {
		t.Fatalf("GetDevice(cfg:ntfy) after RevokeDevice = nil, want still-configured device")
	}
}

func TestNotificationSettings_ReturnsNil(t *testing.T) {
	p := newTestPlugin(nil)

	schemas, err := p.NotificationSettings()
	if err != nil {
		t.Fatalf("NotificationSettings: unexpected error %v", err)
	}
	if schemas != nil {
		t.Fatalf("NotificationSettings = %+v, want nil", schemas)
	}
}

func TestStorageSchema(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin(nil)

	schemas := p.StorageSchema()

	var serviceField *sdk.JsonSchema
	for i := range schemas {
		if schemas[i].Key == "service" {
			serviceField = &schemas[i]
			break
		}
	}
	if serviceField == nil {
		t.Fatalf("expected a %q field in StorageSchema", "service")
	}
	if serviceField.Store == nil || !*serviceField.Store {
		t.Fatalf("service field Store = %v, want true", serviceField.Store)
	}
	if serviceField.Required {
		t.Fatalf("service field must not be Required (empty = not configured yet)")
	}
	found := false
	for _, v := range serviceField.Enum {
		if v == "fake" {
			found = true
		}
	}
	if !found {
		t.Fatalf("service enum %v does not contain %q", serviceField.Enum, "fake")
	}

	var topicField *sdk.JsonSchema
	for i := range schemas {
		if schemas[i].Key == "ntfy_topic" {
			topicField = &schemas[i]
			break
		}
	}
	if topicField == nil {
		t.Fatalf("expected ntfy backend's %q field to be flattened into StorageSchema", "ntfy_topic")
	}
	if topicField.Store == nil || !*topicField.Store {
		t.Fatalf("ntfy_topic field Store = %v, want true", topicField.Store)
	}
}

func TestStorageSchema_BaseURLFieldIsOptionalAndStored(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin(nil)

	schemas := p.StorageSchema()

	var baseURLField *sdk.JsonSchema
	for i := range schemas {
		if schemas[i].Key == "base_url" {
			baseURLField = &schemas[i]
			break
		}
	}
	if baseURLField == nil {
		t.Fatalf("expected a %q field in StorageSchema", "base_url")
	}
	if baseURLField.Required {
		t.Fatalf("base_url field must not be Required (optional)")
	}
	if baseURLField.Store == nil || !*baseURLField.Store {
		t.Fatalf("base_url field Store = %v, want true", baseURLField.Store)
	}
	if len(baseURLField.Condition) != 0 {
		t.Fatalf("base_url field Condition = %+v, want empty (not service-gated)", baseURLField.Condition)
	}
}

func TestStorageSchema_DoesNotMutateBackendSchema(t *testing.T) {
	// Calling StorageSchema() must not leave Store set on the backend's own
	// Schema() return value — each call flattens a copy.
	b, ok := backend.Get("ntfy")
	if !ok {
		t.Fatalf("ntfy backend not registered")
	}

	p := newTestPlugin(nil)
	p.StorageSchema()

	for _, field := range b.Schema() {
		if field.Store != nil {
			t.Fatalf("backend.Schema()[%q].Store = %v after StorageSchema(), want nil (unmutated)", field.Key, *field.Store)
		}
	}
}

// replacingFakeBackend is a fakeBackend that advertises TagReplacer, standing
// in for Telegram/Discord so notifier.go's silent-update policy can be tested
// against both kinds of backend without any network.
type replacingFakeBackend struct {
	fakeBackend
}

func (f *replacingFakeBackend) ID() string    { return "fake-replacer" }
func (f *replacingFakeBackend) Label() string { return "Fake (replacing)" }

func (f *replacingFakeBackend) Schema() []sdk.JsonSchema {
	return []sdk.JsonSchema{
		{
			Type:      sdk.JsonSchemaTypeString,
			Key:       "topic",
			Title:     "Topic",
			Required:  true,
			Condition: []sdk.SchemaCondition{{Key: "service", Value: "fake-replacer"}},
		},
	}
}

func (f *replacingFakeBackend) ReplacesTaggedMessages() bool { return true }

var (
	sharedReplacingBackend     *replacingFakeBackend
	registerReplacingBackendMu sync.Once
)

// registerReplacingFakeBackend mirrors registerFakeBackend for the
// tag-replacing variant: register once per test binary, reset per test.
func registerReplacingFakeBackend() *replacingFakeBackend {
	registerReplacingBackendMu.Do(func() {
		sharedReplacingBackend = &replacingFakeBackend{}
		backend.Register(sharedReplacingBackend)
	})
	sharedReplacingBackend.reset()
	return sharedReplacingBackend
}

// silentUpdate is the republish camera.ui sends once the AI description is
// ready: the tag of the alert it supersedes, plus Silent.
func silentUpdate() *sdk.Notification {
	return &sdk.Notification{
		Title:  "Motion detected",
		Body:   "A person is walking up the driveway.",
		Tag:    "motion:cam-1",
		Silent: true,
	}
}

func TestSendNotification_DeliversSilentUpdateByDefault(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service": "fake",
		"topic":   "sometopic",
	})

	if err := p.SendNotification([]string{"cfg:fake"}, silentUpdate()); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	// Unset config must not drop notifications: the AI description is usually
	// the more informative half of the pair.
	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1 (deliver is the default policy)", got)
	}
}

func TestSendNotification_SkipPolicyDropsSilentUpdate(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":        "fake",
		"topic":          "sometopic",
		"silent_updates": "skip",
	})

	if err := p.SendNotification([]string{"cfg:fake"}, silentUpdate()); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 0 {
		t.Fatalf("Send call count = %d, want 0 (skip policy, backend cannot replace)", got)
	}
}

func TestSendNotification_SkipPolicyStillDeliversTheInitialAlert(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":        "fake",
		"topic":          "sometopic",
		"silent_updates": "skip",
	})

	// Only the follow-up carries Silent; the alert itself always goes out.
	n := &sdk.Notification{Title: "Motion detected", Tag: "motion:cam-1"}
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1", got)
	}
}

func TestSendNotification_SkipPolicyKeepsCriticalUpdate(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":        "fake",
		"topic":          "sometopic",
		"silent_updates": "skip",
	})

	// Silent is ignored for a critical alert, so the skip policy cannot
	// swallow one.
	n := silentUpdate()
	n.Severity = sdk.SeverityCritical
	if err := p.SendNotification([]string{"cfg:fake"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1 (critical ignores Silent)", got)
	}
}

func TestSendNotification_SkipPolicyStillUpdatesReplacingBackend(t *testing.T) {
	fb := registerReplacingFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":        "fake-replacer",
		"topic":          "sometopic",
		"silent_updates": "skip",
	})

	// Skipping exists to avoid a second entry in the list. A backend that
	// edits the original message adds no entry, so it gets the update either
	// way — the user sees one notification whose text improves.
	if err := p.SendNotification([]string{"cfg:fake-replacer"}, silentUpdate()); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 1 {
		t.Fatalf("Send call count = %d, want 1 (a replacing backend edits in place)", got)
	}
}

func TestSendNotification_SkipPolicyDropsUntaggedSilentUpdate(t *testing.T) {
	fb := registerReplacingFakeBackend()
	p := newTestPlugin(map[string]any{
		"service":        "fake-replacer",
		"topic":          "sometopic",
		"silent_updates": "skip",
	})

	// With no tag there is nothing to replace, so even a replacing backend
	// would have to post a second message.
	n := silentUpdate()
	n.Tag = ""
	if err := p.SendNotification([]string{"cfg:fake-replacer"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 0 {
		t.Fatalf("Send call count = %d, want 0 (no tag means no in-place replacement)", got)
	}
}

func TestSilentUpdatePolicy_DefaultsToDeliver(t *testing.T) {
	for _, stored := range []any{nil, "", "deliver", "nonsense", 42} {
		values := map[string]any{}
		if stored != nil {
			values["silent_updates"] = stored
		}
		p := newTestPlugin(values)

		if got := p.silentUpdatePolicy(); got != silentUpdatesDeliver {
			t.Errorf("silentUpdatePolicy() with stored %#v = %q, want %q", stored, got, silentUpdatesDeliver)
		}
	}

	p := newTestPlugin(map[string]any{"silent_updates": silentUpdatesSkip})
	if got := p.silentUpdatePolicy(); got != silentUpdatesSkip {
		t.Errorf("silentUpdatePolicy() = %q, want %q", got, silentUpdatesSkip)
	}
}

func TestStorageSchema_HasSilentUpdatesField(t *testing.T) {
	p := newTestPlugin(nil)

	var field *sdk.JsonSchema
	for i, f := range p.StorageSchema() {
		if f.Key == "silent_updates" {
			field = &p.StorageSchema()[i]
			break
		}
	}
	if field == nil {
		t.Fatalf("expected a %q field in StorageSchema", "silent_updates")
	}
	if field.DefaultValue != silentUpdatesDeliver {
		t.Errorf("DefaultValue = %v, want %q", field.DefaultValue, silentUpdatesDeliver)
	}
	if len(field.Enum) != 2 {
		t.Errorf("Enum = %v, want the two policy values", field.Enum)
	}
	for _, v := range field.Enum {
		if field.EnumLabels[v] == "" {
			t.Errorf("Enum value %q has no EnumLabels entry, so the UI would show the raw value", v)
		}
	}
	if field.Store == nil || !*field.Store {
		t.Errorf("Store = %v, want true", field.Store)
	}
	if len(field.Condition) != 0 {
		t.Errorf("Condition = %+v, want empty (applies to every service)", field.Condition)
	}
}
