package main

import (
	"context"
	"fmt"
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
// dispatch logic without depending on any real delivery backend (those land
// in Tasks 5-7). It self-registers once under id "fake" via
// registerFakeBackend, and its mutable test knobs (failTopics) are guarded
// by mu so tests can run sequentially against the single shared instance.
type fakeBackend struct {
	mu         sync.Mutex
	calls      []fakeSendCall
	failTopics map[string]bool
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
	}
}

func (f *fakeBackend) ParseTarget(input map[string]any) (map[string]string, error) {
	topic, _ := input["topic"].(string)
	if topic == "" {
		return nil, fmt.Errorf("topic is required")
	}
	return map[string]string{"topic": topic}, nil
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
// binary), then resets its recorded state for the calling test.
func registerFakeBackend() *fakeBackend {
	registerFakeBackendMu.Do(func() {
		sharedFakeBackend = &fakeBackend{}
		backend.Register(sharedFakeBackend)
	})
	sharedFakeBackend.reset()
	return sharedFakeBackend
}

// newTestPlugin builds a NotifyPlugin wired to an in-memory device store, so
// tests never touch a real *sdk.DeviceStorage.
func newTestPlugin() *NotifyPlugin {
	return &NotifyPlugin{
		BasePlugin: sdk.NewBasePlugin(&sdk.Logger{}, nil, nil),
		store:      newDeviceStore(newFakeStorage()),
	}
}

func TestRegisterDevice_Valid(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	dev, err := p.RegisterDevice("u1", map[string]any{"service": "fake", "topic": "t"})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if dev == nil {
		t.Fatalf("RegisterDevice: got nil device")
	}
	if dev.OwnerUserID != "u1" {
		t.Fatalf("OwnerUserID = %q, want u1", dev.OwnerUserID)
	}
	if !dev.Active {
		t.Fatalf("expected new device to be Active")
	}
	if dev.ID == "" {
		t.Fatalf("expected non-empty ID")
	}
	if svc, ok := dev.Metadata["service"].(string); !ok || svc != "fake" {
		t.Fatalf("Metadata[service] = %#v, want string \"fake\"", dev.Metadata["service"])
	}
	if topic, ok := dev.Metadata["topic"].(string); !ok || topic != "t" {
		t.Fatalf("Metadata[topic] = %#v, want string \"t\"", dev.Metadata["topic"])
	}

	stored, ok := p.store.Get(dev.ID)
	if !ok {
		t.Fatalf("expected device persisted in store")
	}
	if stored.Name != "fake" {
		t.Fatalf("Name = %q, want fallback to service %q", stored.Name, "fake")
	}
}

func TestRegisterDevice_UnknownService(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	_, err := p.RegisterDevice("u1", map[string]any{"service": "not-a-real-service", "topic": "t"})
	if err == nil {
		t.Fatalf("expected error for unknown service")
	}
}

func TestRegisterDevice_MissingRequiredField(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	_, err := p.RegisterDevice("u1", map[string]any{"service": "fake"})
	if err == nil {
		t.Fatalf("expected error for missing topic")
	}
}

func TestGetDevices_OwnerFilter(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	if _, err := p.RegisterDevice("u1", map[string]any{"service": "fake", "topic": "t1"}); err != nil {
		t.Fatalf("RegisterDevice u1: %v", err)
	}

	got, err := p.GetDevices([]string{"u1"})
	if err != nil {
		t.Fatalf("GetDevices(u1): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("GetDevices(u1) = %d devices, want 1", len(got))
	}

	got, err = p.GetDevices([]string{"u2"})
	if err != nil {
		t.Fatalf("GetDevices(u2): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("GetDevices(u2) = %d devices, want 0", len(got))
	}
}

func TestUpdateDevice_ActiveToggle(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	dev, err := p.RegisterDevice("u1", map[string]any{"service": "fake", "topic": "t"})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	updated, err := p.UpdateDevice(dev.ID, map[string]any{"active": false})
	if err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}
	if updated == nil {
		t.Fatalf("UpdateDevice: got nil")
	}
	if updated.Active {
		t.Fatalf("expected Active=false after update")
	}

	stored, ok := p.store.Get(dev.ID)
	if !ok {
		t.Fatalf("expected device still stored")
	}
	if stored.Active {
		t.Fatalf("expected persisted Active=false")
	}
}

func TestUpdateDevice_UnknownID(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	updated, err := p.UpdateDevice("nope", map[string]any{"active": false})
	if err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}
	if updated != nil {
		t.Fatalf("UpdateDevice(unknown) = %+v, want nil", updated)
	}
}

func TestRevokeDevice(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	dev, err := p.RegisterDevice("u1", map[string]any{"service": "fake", "topic": "t"})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}

	if err := p.RevokeDevice(dev.ID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	if _, ok := p.store.Get(dev.ID); ok {
		t.Fatalf("expected device removed after RevokeDevice")
	}

	got, err := p.GetDevice(dev.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got != nil {
		t.Fatalf("GetDevice(revoked) = %+v, want nil", got)
	}
}

func TestSendNotification_PartialFailureDoesNotAbort(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin()

	okDev, err := p.RegisterDevice("u1", map[string]any{"service": "fake", "topic": "ok-topic"})
	if err != nil {
		t.Fatalf("RegisterDevice ok: %v", err)
	}
	failDev, err := p.RegisterDevice("u1", map[string]any{"service": "fake", "topic": "fail-topic"})
	if err != nil {
		t.Fatalf("RegisterDevice fail: %v", err)
	}
	fb.failTopics["fail-topic"] = true

	n := &sdk.Notification{Title: "hello", Body: "world"}
	err = p.SendNotification([]string{okDev.ID, failDev.ID}, n)
	if err == nil {
		t.Fatalf("expected joined error from the failing device")
	}
	if !strings.Contains(err.Error(), "fail-topic") {
		t.Fatalf("error %v does not reference the failing device/topic", err)
	}
	if got := fb.callCount(); got != 2 {
		t.Fatalf("Send call count = %d, want 2 (both devices dispatched)", got)
	}
}

func TestSendNotification_SkipsInactiveAndUnknown(t *testing.T) {
	fb := registerFakeBackend()
	p := newTestPlugin()

	dev, err := p.RegisterDevice("u1", map[string]any{"service": "fake", "topic": "t"})
	if err != nil {
		t.Fatalf("RegisterDevice: %v", err)
	}
	if _, err := p.UpdateDevice(dev.ID, map[string]any{"active": false}); err != nil {
		t.Fatalf("UpdateDevice: %v", err)
	}

	n := &sdk.Notification{Title: "hello"}
	if err := p.SendNotification([]string{dev.ID, "does-not-exist"}, n); err != nil {
		t.Fatalf("SendNotification: unexpected error %v", err)
	}
	if got := fb.callCount(); got != 0 {
		t.Fatalf("Send call count = %d, want 0 for inactive/unknown devices", got)
	}
}

func TestNotificationSettings(t *testing.T) {
	registerFakeBackend()
	p := newTestPlugin()

	schemas, err := p.NotificationSettings()
	if err != nil {
		t.Fatalf("NotificationSettings: %v", err)
	}

	var serviceField *sdk.JsonSchema
	for i := range schemas {
		if schemas[i].Key == "service" {
			serviceField = &schemas[i]
			break
		}
	}
	if serviceField == nil {
		t.Fatalf("expected a %q field in schema", "service")
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
	if !serviceField.Required {
		t.Fatalf("expected service field to be Required")
	}

	foundTopic := false
	for _, s := range schemas {
		if s.Key == "topic" {
			foundTopic = true
		}
	}
	if !foundTopic {
		t.Fatalf("expected fake backend's %q schema field to be included", "topic")
	}
}
