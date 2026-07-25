package main

import (
	sdk "github.com/cameraui/sdk/go"
)

// devicesStorageKey is the single DeviceStorage key this plugin persists
// to: the full set of registered NotifierDevice records, JSON-marshaled by
// the device store (see Task 3's src/store.go). Declared here, in
// StorageSchema, because sdk.DeviceStorage.SetValue silently no-ops for any
// key with no declared schema (Global Constraints) — every later task that
// writes to this key depends on it being declared exactly once, here.
const devicesStorageKey = "devices"

// Compile-time conformance guards. NotifyPlugin must satisfy both sdk.Plugin
// (the camera lifecycle hooks) and sdk.NotifierInterface (the Notifier RPC
// surface implemented across src/notifier.go) — these fail `go build` loudly
// on any future signature drift instead of surfacing as a silent RPC
// registration gap discovered only at runtime.
var _ sdk.NotifierInterface = (*NotifyPlugin)(nil)
var _ sdk.Plugin = (*NotifyPlugin)(nil)

// NotifyPlugin is the camera.ui plugin entrypoint for Notify. It implements
// sdk.Plugin (the camera lifecycle hooks, all no-ops here — Notify is a
// PluginRoleHub that owns no cameras) and, from Task 4 on, sdk.
// NotifierInterface (GetDevices/RegisterDevice/SendNotification/... —
// contract.ts declares PluginInterface.Notifier). This task only lays down
// the struct, construction, and the two SDK-required allow-lists
// (RPCMethods, StorageSchema); the Notifier methods themselves are Task 4.
type NotifyPlugin struct {
	sdk.BasePlugin

	// store is the persisted registry of this plugin's NotifierDevice
	// records (Task 3's src/store.go), backed by BasePlugin.Storage under
	// devicesStorageKey. Task 4's Notifier RPC methods (src/notifier.go)
	// read and write through it exclusively.
	store *deviceStore
}

// NewPlugin constructs the plugin. Signature is fixed by sdk.Run's
// pluginConstructor type (see main.go).
func NewPlugin(logger *sdk.Logger, api *sdk.PluginAPI, storage *sdk.DeviceStorage) sdk.Plugin {
	return &NotifyPlugin{
		BasePlugin: sdk.NewBasePlugin(logger, api, storage),
		store:      newDeviceStore(storage),
	}
}

// ConfigureCameras satisfies sdk.Plugin. Notify is a Hub that attaches to
// no cameras (contract.ts: provides/consumes are both empty) — every
// camera-lifecycle hook is a no-op.
func (p *NotifyPlugin) ConfigureCameras(cameras []*sdk.CameraDevice) error {
	return nil
}

// OnCameraAdded satisfies sdk.Plugin. See ConfigureCameras.
func (p *NotifyPlugin) OnCameraAdded(camera *sdk.CameraDevice) error {
	return nil
}

// OnCameraReleased satisfies sdk.Plugin. See ConfigureCameras.
func (p *NotifyPlugin) OnCameraReleased(cameraID string) error {
	return nil
}

// RPCMethods is the RPCMethodAllowlist the SDK's rpc layer checks before
// registering any exported method as a wire-callable subject (Global
// Constraints: "every wire name MUST be listed in RPCMethods() or it is
// never registered"). Lists the seven Notifier wire names (wire name =
// first-letter-lowercased Go method name) implemented in src/notifier.go.
func (p *NotifyPlugin) RPCMethods() []string {
	return []string{
		"getDevices",
		"getDevice",
		"registerDevice",
		"revokeDevice",
		"updateDevice",
		"sendNotification",
		"notificationSettings",
	}
}

// StorageSchema declares this plugin's persisted storage keys. Required per
// Global Constraints: sdk.DeviceStorage.SetValue silently no-ops for a key
// with no declared schema, so devicesStorageKey must be declared before
// Task 3's device store ever writes to it. Hidden because it's an
// internal, plugin-managed blob (the JSON-encoded []sdk.NotifierDevice
// slice), not a user-facing settings field.
func (p *NotifyPlugin) StorageSchema() []sdk.JsonSchema {
	storeTrue := true
	return []sdk.JsonSchema{
		{
			Type:   sdk.JsonSchemaTypeString,
			Key:    devicesStorageKey,
			Title:  "Devices",
			Hidden: true,
			Store:  &storeTrue,
		},
	}
}
