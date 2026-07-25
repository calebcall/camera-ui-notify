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

// NotifyPlugin is the camera.ui plugin entrypoint for Notify. It implements
// sdk.Plugin (the camera lifecycle hooks, all no-ops here — Notify is a
// PluginRoleHub that owns no cameras) and, from Task 4 on, sdk.
// NotifierInterface (GetDevices/RegisterDevice/SendNotification/... —
// contract.ts declares PluginInterface.Notifier). This task only lays down
// the struct, construction, and the two SDK-required allow-lists
// (RPCMethods, StorageSchema); the Notifier methods themselves are Task 4.
type NotifyPlugin struct {
	sdk.BasePlugin
}

// NewPlugin constructs the plugin. Signature is fixed by sdk.Run's
// pluginConstructor type (see main.go).
func NewPlugin(logger *sdk.Logger, api *sdk.PluginAPI, storage *sdk.DeviceStorage) sdk.Plugin {
	return &NotifyPlugin{
		BasePlugin: sdk.NewBasePlugin(logger, api, storage),
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
// never registered"). Empty for this task — the seven Notifier wire methods
// (getDevices, getDevice, registerDevice, revokeDevice, updateDevice,
// sendNotification, notificationSettings) are added in Task 4 once they're
// implemented in src/notifier.go.
func (p *NotifyPlugin) RPCMethods() []string {
	return []string{}
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
