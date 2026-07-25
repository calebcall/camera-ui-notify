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
// dispatch logic without depending on any real delivery backend. It
// self-registers once under id "fake" via registerFakeBackend, and its
// mutable test knobs (failTopics) are guarded by mu so tests can run
// sequentially against the single shared instance.
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
