package main

import (
	"sync"

	sdk "github.com/cameraui/sdk/go"

	"github.com/calebcall/camera-ui-notify/src/backend"
)

// Compile-time conformance guards. NotifyPlugin must satisfy both sdk.Plugin
// (the camera lifecycle hooks) and sdk.NotifierInterface (the Notifier RPC
// surface implemented across src/notifier.go) — these fail `go build` loudly
// on any future signature drift instead of surfacing as a silent RPC
// registration gap discovered only at runtime.
var _ sdk.NotifierInterface = (*NotifyPlugin)(nil)
var _ sdk.Plugin = (*NotifyPlugin)(nil)

// NotifyPlugin is the camera.ui plugin entrypoint for Notify. It implements
// sdk.Plugin (the camera lifecycle hooks, all no-ops here — Notify is a
// PluginRoleHub that owns no cameras) and sdk.NotifierInterface
// (GetDevices/RegisterDevice/SendNotification/... — contract.ts declares
// PluginInterface.Notifier).
//
// R1 pivot: the stock camera.ui UI has no way to register a notifier
// device (see docs/superpowers/plans/2026-07-24-notify-config-targets.md),
// so this plugin no longer maintains a device registry. Instead it holds a
// single config-driven target in its own plugin storage (StorageSchema,
// below) and src/notifier.go synthesizes the one NotifierDevice from that
// config on every read.
type NotifyPlugin struct {
	sdk.BasePlugin

	// Spike (#12) state; remove with src/diagnostics.go.
	diagOnce sync.Once
	diag     *diagRuntime
}

// NewPlugin constructs the plugin. Signature is fixed by sdk.Run's
// pluginConstructor type (see main.go).
func NewPlugin(logger *sdk.Logger, api *sdk.PluginAPI, storage *sdk.DeviceStorage) sdk.Plugin {
	p := &NotifyPlugin{
		BasePlugin: sdk.NewBasePlugin(logger, api, storage),
	}
	// Spike (#12): release detection-event subscriptions on teardown.
	if api != nil {
		api.On(string(sdk.APIEventShutdown), func(...any) {
			p.DiagShutdown()
		})
	}
	return p
}

// ConfigureCameras satisfies sdk.Plugin. Notify is a Hub that attaches to
// no cameras (contract.ts: provides/consumes are both empty) — every
// camera-lifecycle hook is a no-op.
func (p *NotifyPlugin) ConfigureCameras(cameras []*sdk.CameraDevice) error {
	// Spike (#12): record whether the host assigns this plugin any cameras.
	p.diagCameras(cameras)
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

// StorageSchema declares this plugin's persisted config: which delivery
// service is active plus that service's own settings. It is the plugin's
// config-tab form (views/Plugin.vue renders any StorageSchema as an
// editable+savable form via CuiSchema/setConfig) — this is what R1 replaced
// the old device registry with, since the stock UI never gave users a way
// to register a NotifierDevice directly.
//
// The "service" field lists every registered backend.All() id and is not
// Required: an empty value simply means "not configured yet" (getDevices
// then returns no devices rather than erroring). Every backend's own
// Schema() fields are flattened in below it — those fields are already
// namespaced (ntfy_server, gotify_token, ...) and Condition-gated on
// "service" (see e.g. src/backend/ntfy.go), so the plugin page only shows
// the fields for whichever service is currently selected. Each flattened
// field gets Store set on a copy (never mutating the backend's own
// returned slice/elements) since, unlike a device-registration payload,
// this config is now persisted directly in plugin storage.
func (p *NotifyPlugin) StorageSchema() []sdk.JsonSchema {
	storeTrue := true

	backends := backend.All()
	ids := make([]string, 0, len(backends))
	for _, b := range backends {
		ids = append(ids, b.ID())
	}

	fields := []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "service",
			Title:       "Service",
			Description: "The notification delivery service to send to.",
			Enum:        ids,
			Store:       &storeTrue,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "base_url",
			Title:       "camera.ui Base URL",
			Placeholder: "https://camera.example.com",
			Description: "Optional. Used to turn camera.ui's relative deep links into absolute tap-through URLs (e.g. ntfy Click).",
			Store:       &storeTrue,
		},
		{
			Type:        sdk.JsonSchemaTypeBoolean,
			Key:         "diagnostics",
			Title:       "Diagnostics (temporary)",
			Description: "Logs notification payloads and detection events to diagnose AI-description availability. Logs camera and zone names; leave off unless asked to enable it.",
			Store:       &storeTrue,
		},
	}

	for _, b := range backends {
		for _, field := range b.Schema() {
			field.Store = &storeTrue
			fields = append(fields, field)
		}
	}

	return fields
}
