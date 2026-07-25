package main

import (
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

// fakeStorage is an in-memory stand-in for a *sdk.DeviceStorage, mirroring
// the NVR recorder's fakeCameraStorage (see
// camera-ui-nvr-local/src/recorder/manager_test.go): it implements the
// deviceStorage subset (GetValue/SetValue) over a plain map, with no schema
// validation — deviceStore is the only thing under test here.
type fakeStorage struct {
	values map[string]any
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{values: make(map[string]any)}
}

func (f *fakeStorage) GetValue(key string, defaultValue ...any) any {
	if v, ok := f.values[key]; ok {
		return v
	}
	if len(defaultValue) > 0 {
		return defaultValue[0]
	}
	return nil
}

func (f *fakeStorage) SetValue(key string, value any) error {
	f.values[key] = value
	return nil
}

func devicesEqualByID(devs []sdk.NotifierDevice, ids ...string) bool {
	if len(devs) != len(ids) {
		return false
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	for _, d := range devs {
		if _, ok := want[d.ID]; !ok {
			return false
		}
	}
	return true
}

func TestDeviceStore_PutThenGet(t *testing.T) {
	store := newDeviceStore(newFakeStorage())

	dev := sdk.NotifierDevice{
		ID:          "dev-1",
		OwnerUserID: "u1",
		Name:        "Phone",
		Active:      true,
		Metadata:    map[string]any{"service": "ntfy", "topic": "alerts"},
	}

	if err := store.Put(dev); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, ok := store.Get("dev-1")
	if !ok {
		t.Fatalf("Get: expected device to exist")
	}
	if got.ID != dev.ID || got.OwnerUserID != dev.OwnerUserID || got.Name != dev.Name || got.Active != dev.Active {
		t.Fatalf("Get: got %+v, want %+v", got, dev)
	}
	if got.Metadata["service"] != "ntfy" || got.Metadata["topic"] != "alerts" {
		t.Fatalf("Get: metadata mismatch: %+v", got.Metadata)
	}
}

func TestDeviceStore_Get_Missing(t *testing.T) {
	store := newDeviceStore(newFakeStorage())

	if _, ok := store.Get("nope"); ok {
		t.Fatalf("Get: expected false for missing device")
	}
}

func TestDeviceStore_List_NilOrEmptyReturnsAll(t *testing.T) {
	store := newDeviceStore(newFakeStorage())

	if err := store.Put(sdk.NotifierDevice{ID: "d1", OwnerUserID: "u1"}); err != nil {
		t.Fatalf("Put d1: %v", err)
	}
	if err := store.Put(sdk.NotifierDevice{ID: "d2", OwnerUserID: "u2"}); err != nil {
		t.Fatalf("Put d2: %v", err)
	}

	if got := store.List(nil); !devicesEqualByID(got, "d1", "d2") {
		t.Fatalf("List(nil) = %+v, want all devices", got)
	}
	if got := store.List([]string{}); !devicesEqualByID(got, "d1", "d2") {
		t.Fatalf("List([]) = %+v, want all devices", got)
	}
}

func TestDeviceStore_List_FiltersByOwner(t *testing.T) {
	store := newDeviceStore(newFakeStorage())

	if err := store.Put(sdk.NotifierDevice{ID: "d1", OwnerUserID: "u1"}); err != nil {
		t.Fatalf("Put d1: %v", err)
	}
	if err := store.Put(sdk.NotifierDevice{ID: "d2", OwnerUserID: "u2"}); err != nil {
		t.Fatalf("Put d2: %v", err)
	}
	if err := store.Put(sdk.NotifierDevice{ID: "d3", OwnerUserID: "u1"}); err != nil {
		t.Fatalf("Put d3: %v", err)
	}

	got := store.List([]string{"u1"})
	if !devicesEqualByID(got, "d1", "d3") {
		t.Fatalf("List([u1]) = %+v, want d1+d3", got)
	}
}

func TestDeviceStore_Delete(t *testing.T) {
	store := newDeviceStore(newFakeStorage())

	if err := store.Put(sdk.NotifierDevice{ID: "d1", OwnerUserID: "u1"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	existed, err := store.Delete("d1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !existed {
		t.Fatalf("Delete: expected true on first delete")
	}

	existed, err = store.Delete("d1")
	if err != nil {
		t.Fatalf("Delete (second): %v", err)
	}
	if existed {
		t.Fatalf("Delete: expected false on second delete")
	}

	if _, ok := store.Get("d1"); ok {
		t.Fatalf("Get after Delete: expected device gone")
	}
}

// TestDeviceStore_PersistsAcrossInstances verifies write-through persistence:
// a brand-new deviceStore over the same underlying storage must see devices
// written by a prior store, round-tripping through the "devices" key as
// JSON. Metadata uses string values (matching how devices actually store
// backend config), sidestepping the float64-vs-int JSON round-trip quirk for
// numeric values.
func TestDeviceStore_PersistsAcrossInstances(t *testing.T) {
	backing := newFakeStorage()
	store1 := newDeviceStore(backing)

	dev := sdk.NotifierDevice{
		ID:          "dev-1",
		OwnerUserID: "u1",
		Name:        "Phone",
		Active:      true,
		Metadata:    map[string]any{"service": "gotify", "server": "https://gotify.example", "token": "abc123"},
	}
	if err := store1.Put(dev); err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, ok := backing.values[devicesStorageKey]
	if !ok {
		t.Fatalf("expected %q key to be persisted", devicesStorageKey)
	}
	if _, ok := raw.(string); !ok {
		t.Fatalf("expected persisted value to be a JSON string, got %T", raw)
	}

	store2 := newDeviceStore(backing)
	got, ok := store2.Get("dev-1")
	if !ok {
		t.Fatalf("new store did not see persisted device")
	}
	if got.Name != "Phone" || got.OwnerUserID != "u1" || !got.Active {
		t.Fatalf("new store got wrong device: %+v", got)
	}
	if got.Metadata["service"] != "gotify" || got.Metadata["server"] != "https://gotify.example" || got.Metadata["token"] != "abc123" {
		t.Fatalf("new store metadata mismatch: %+v", got.Metadata)
	}

	list := store2.List(nil)
	if !devicesEqualByID(list, "dev-1") {
		t.Fatalf("new store List(nil) = %+v, want [dev-1]", list)
	}
}
