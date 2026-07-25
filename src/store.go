package main

import (
	"encoding/json"
	"sync"

	sdk "github.com/cameraui/sdk/go"
)

// deviceStorage is the subset of *sdk.DeviceStorage that deviceStore
// depends on, so tests can substitute an in-memory fake (see
// fakeStorage in store_test.go) instead of constructing a real
// *sdk.DeviceStorage, which requires internals not exported outside
// package sdk.
type deviceStorage interface {
	GetValue(key string, defaultValue ...any) any
	SetValue(key string, value any) error
}

// deviceStore is the in-memory + persisted registry of this plugin's
// NotifierDevice records. It is the single source of truth Task 4's RPC
// methods read and write through. The full device set is JSON-marshaled as
// one blob under devicesStorageKey (declared in plugin.go's StorageSchema)
// rather than one storage key per device, since sdk.DeviceStorage schemas
// are declared once at construction time and devices are created/removed
// dynamically at runtime.
type deviceStore struct {
	storage deviceStorage

	mu      sync.Mutex
	loaded  bool
	devices map[string]sdk.NotifierDevice
}

// newDeviceStore constructs a deviceStore over s. Loading is lazy (on first
// access) rather than here, so construction never fails and never blocks on
// storage I/O.
func newDeviceStore(s deviceStorage) *deviceStore {
	return &deviceStore{storage: s}
}

// load populates d.devices from storage the first time it's needed. Callers
// must hold d.mu.
func (d *deviceStore) load() {
	if d.loaded {
		return
	}
	d.devices = make(map[string]sdk.NotifierDevice)
	d.loaded = true

	raw := d.storage.GetValue(devicesStorageKey)
	str, ok := raw.(string)
	if !ok || str == "" {
		return
	}

	var list []sdk.NotifierDevice
	if err := json.Unmarshal([]byte(str), &list); err != nil {
		// Corrupt or foreign value under this key: treat as empty rather
		// than fail construction/lookups. Put/Delete will overwrite it with
		// well-formed JSON on the next write.
		return
	}
	for _, dev := range list {
		d.devices[dev.ID] = dev
	}
}

// persistLocked marshals the full device set and writes it through to
// storage. Callers must hold d.mu.
func (d *deviceStore) persistLocked() error {
	list := make([]sdk.NotifierDevice, 0, len(d.devices))
	for _, dev := range d.devices {
		list = append(list, dev)
	}

	encoded, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return d.storage.SetValue(devicesStorageKey, string(encoded))
}

// List returns every device owned by one of ownerUserIDs. A nil or empty
// slice returns every device.
func (d *deviceStore) List(ownerUserIDs []string) []sdk.NotifierDevice {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()

	out := make([]sdk.NotifierDevice, 0, len(d.devices))
	if len(ownerUserIDs) == 0 {
		for _, dev := range d.devices {
			out = append(out, dev)
		}
		return out
	}

	owners := make(map[string]struct{}, len(ownerUserIDs))
	for _, id := range ownerUserIDs {
		owners[id] = struct{}{}
	}
	for _, dev := range d.devices {
		if _, ok := owners[dev.OwnerUserID]; ok {
			out = append(out, dev)
		}
	}
	return out
}

// Get fetches a single device by id.
func (d *deviceStore) Get(id string) (sdk.NotifierDevice, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()

	dev, ok := d.devices[id]
	return dev, ok
}

// Put upserts dev (matched by ID) and persists the full device set.
func (d *deviceStore) Put(dev sdk.NotifierDevice) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()

	d.devices[dev.ID] = dev
	return d.persistLocked()
}

// Delete removes the device with the given id and persists the result.
// Returns whether a device with that id existed.
func (d *deviceStore) Delete(id string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.load()

	if _, ok := d.devices[id]; !ok {
		return false, nil
	}
	delete(d.devices, id)
	return true, d.persistLocked()
}
