package main

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/cameraui/sdk/go"

	"github.com/calebcall/camera-ui-notify/src/backend"
)

// notifierDeviceIDPrefix prefixes the selected service id to form the single
// synthesized device's stable id (e.g. "cfg:ntfy"). Stable across calls
// because it's derived purely from the currently configured service, not
// from any generated identifier.
const notifierDeviceIDPrefix = "cfg:"

// configuredDevice reads the plugin's persisted "service" selection and that
// backend's own namespaced config fields, and synthesizes the single
// NotifierDevice this plugin exposes. Returns (nil, nil) — not an error —
// both when nothing is configured yet (empty/unknown service) and when the
// stored config is incomplete (ParseTarget rejects it, e.g. a required field
// left blank): both are "not configured yet" from the caller's perspective.
func (p *NotifyPlugin) configuredDevice(ownerUserID string) (*sdk.NotifierDevice, error) {
	service, _ := p.Storage.GetValue("service", "").(string)
	if service == "" {
		return nil, nil
	}

	b, ok := backend.Get(service)
	if !ok {
		return nil, nil
	}

	schema := b.Schema()
	input := make(map[string]any, len(schema))
	for _, field := range schema {
		input[field.Key] = p.Storage.GetValue(field.Key, "")
	}

	cfg, err := b.ParseTarget(input)
	if err != nil {
		return nil, nil
	}

	metadata := make(map[string]any, len(cfg)+1)
	metadata["service"] = service
	for k, v := range cfg {
		metadata[k] = v
	}

	return &sdk.NotifierDevice{
		ID:          notifierDeviceIDPrefix + service,
		OwnerUserID: ownerUserID,
		Name:        b.Label(),
		Active:      true,
		Metadata:    metadata,
	}, nil
}

// GetDevices implements sdk.NotifierInterface. This plugin has no per-user
// device registry: it exposes exactly one instance-global target,
// synthesized from plugin config by configuredDevice.
//
// Ownership note: the host's notify() filters GetDevices results by
// d.OwnerUserID being a member of ownerUserIDs (the users eligible for this
// particular notification), and never calls GetDevices with an empty
// eligible set (it returns earlier in that case). So stamping the
// synthesized device's OwnerUserID with ownerUserIDs[0] whenever the slice
// is non-empty is always safe and delivers the one configured target
// exactly once per notify() call — it is never compared against any other
// element of ownerUserIDs. When ownerUserIDs is empty (e.g. an admin-UI
// listing call with no specific eligible user), OwnerUserID is left blank.
func (p *NotifyPlugin) GetDevices(ownerUserIDs []string) ([]sdk.NotifierDevice, error) {
	owner := ""
	if len(ownerUserIDs) > 0 {
		owner = ownerUserIDs[0]
	}

	dev, err := p.configuredDevice(owner)
	if err != nil || dev == nil {
		return []sdk.NotifierDevice{}, err
	}
	return []sdk.NotifierDevice{*dev}, nil
}

// GetDevice implements sdk.NotifierInterface. Returns (nil, nil) when id
// isn't the current synthesized device's id, per the interface contract, so
// the manager can probe the next notifier plugin.
func (p *NotifyPlugin) GetDevice(deviceID string) (*sdk.NotifierDevice, error) {
	service, _ := p.Storage.GetValue("service", "").(string)
	if service == "" || deviceID != notifierDeviceIDPrefix+service {
		return nil, nil
	}
	return p.configuredDevice("")
}

// RegisterDevice implements sdk.NotifierInterface. Targets are configured on
// the plugin's own settings page (StorageSchema), not registered at
// runtime — the stock UI never exposed a way to register a NotifierDevice
// for this plugin anyway (see docs/superpowers/plans/
// 2026-07-24-notify-config-targets.md). This method exists only to satisfy
// the interface.
func (p *NotifyPlugin) RegisterDevice(ownerUserID string, input map[string]any) (*sdk.NotifierDevice, error) {
	return nil, errors.New("notify targets are configured in the plugin settings, not registered")
}

// RevokeDevice implements sdk.NotifierInterface. Devices are config-driven,
// not registered, so there is nothing to revoke; always a no-op success.
func (p *NotifyPlugin) RevokeDevice(deviceID string) error {
	return nil
}

// UpdateDevice implements sdk.NotifierInterface. Devices are config-driven,
// not registered, so there is nothing to patch; benign no-op.
func (p *NotifyPlugin) UpdateDevice(deviceID string, patch map[string]any) (*sdk.NotifierDevice, error) {
	return nil, nil
}

// SendNotification implements sdk.NotifierInterface. Each device id is
// resolved independently via GetDevice (the id must match the currently
// synthesized device); an unknown id, an inactive device, or a device whose
// backend is no longer registered is skipped (and logged, for the latter);
// a backend Send failure is logged and joined into the returned error, but
// never aborts the remaining devices in the fan-out.
func (p *NotifyPlugin) SendNotification(deviceIDs []string, n *sdk.Notification) error {
	var errs []error

	for _, id := range deviceIDs {
		dev, err := p.GetDevice(id)
		if err != nil || dev == nil || !dev.Active {
			continue
		}

		service, _ := dev.Metadata["service"].(string)
		b, ok := backend.Get(service)
		if !ok {
			if p.Logger != nil {
				p.Logger.Warn(fmt.Sprintf("notify: device %s references unknown service %q, skipping", id, service))
			}
			continue
		}

		cfg := make(map[string]string, len(dev.Metadata))
		for k, v := range dev.Metadata {
			if k == "service" {
				continue
			}
			if s, ok := v.(string); ok {
				cfg[k] = s
			}
		}

		if err := b.Send(context.Background(), cfg, *n); err != nil {
			wrapped := fmt.Errorf("device %s (%s): %w", id, service, err)
			if p.Logger != nil {
				p.Logger.Error(fmt.Sprintf("notify: send failed: %v", wrapped))
			}
			errs = append(errs, wrapped)
		}
	}

	return errors.Join(errs...)
}

// NotificationSettings implements sdk.NotifierInterface. The service
// selection and its config now live on the plugin's config tab
// (StorageSchema); duplicating that form in the "send test" panel would
// only confuse. The panel still lists the synthesized device as a target
// via GetDevices.
func (p *NotifyPlugin) NotificationSettings() ([]sdk.JsonSchema, error) {
	return nil, nil
}
