package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	sdk "github.com/cameraui/sdk/go"

	"github.com/calebcall/camera-ui-notify/src/backend"
)

// GetDevices implements sdk.NotifierInterface. It returns every device this
// plugin knows about for the given owners (all devices when ownerUserIDs is
// nil/empty — see deviceStore.List).
func (p *NotifyPlugin) GetDevices(ownerUserIDs []string) ([]sdk.NotifierDevice, error) {
	return p.store.List(ownerUserIDs), nil
}

// GetDevice implements sdk.NotifierInterface. Returns (nil, nil) when the id
// isn't ours, per the interface contract, so the manager can probe the next
// notifier plugin.
func (p *NotifyPlugin) GetDevice(deviceID string) (*sdk.NotifierDevice, error) {
	dev, ok := p.store.Get(deviceID)
	if !ok {
		return nil, nil
	}
	return &dev, nil
}

// RegisterDevice implements sdk.NotifierInterface. input must carry a
// "service" key naming a registered backend; the remaining fields are
// validated and normalized by that backend's ParseTarget. The resulting
// device's Metadata holds "service" plus every ParseTarget config key, all
// as strings, so it round-trips cleanly through JSON storage (deviceStore)
// without ever smuggling a non-string value into Metadata.
func (p *NotifyPlugin) RegisterDevice(ownerUserID string, input map[string]any) (*sdk.NotifierDevice, error) {
	service, _ := input["service"].(string)
	b, ok := backend.Get(service)
	if !ok {
		return nil, fmt.Errorf("unknown service %q", service)
	}

	cfg, err := b.ParseTarget(input)
	if err != nil {
		return nil, err
	}

	name, _ := input["name"].(string)
	if name == "" {
		name = service
	}

	metadata := make(map[string]any, len(cfg)+1)
	metadata["service"] = service
	for k, v := range cfg {
		metadata[k] = v
	}

	dev := sdk.NotifierDevice{
		ID:          uuid.NewString(),
		OwnerUserID: ownerUserID,
		Name:        name,
		Active:      true,
		Metadata:    metadata,
	}

	if err := p.store.Put(dev); err != nil {
		return nil, err
	}
	return &dev, nil
}

// RevokeDevice implements sdk.NotifierInterface. Deleting an unknown id is
// not an error (Delete's bool return is not surfaced here — the manager
// only cares whether the call succeeded).
func (p *NotifyPlugin) RevokeDevice(deviceID string) error {
	_, err := p.store.Delete(deviceID)
	return err
}

// UpdateDevice implements sdk.NotifierInterface. Only the plugin-agnostic
// "name"/"active" patch keys are applied; unknown keys are ignored per the
// interface contract. Returns (nil, nil) when the id isn't ours.
func (p *NotifyPlugin) UpdateDevice(deviceID string, patch map[string]any) (*sdk.NotifierDevice, error) {
	dev, ok := p.store.Get(deviceID)
	if !ok {
		return nil, nil
	}

	if name, ok := patch["name"].(string); ok {
		dev.Name = name
	}
	if active, ok := patch["active"].(bool); ok {
		dev.Active = active
	}

	if err := p.store.Put(dev); err != nil {
		return nil, err
	}
	return &dev, nil
}

// SendNotification implements sdk.NotifierInterface. Each device id is
// dispatched independently: an unknown id, an inactive device, or a device
// whose backend is no longer registered is skipped (and logged, for the
// latter); a backend Send failure is logged and joined into the returned
// error, but never aborts the remaining devices in the fan-out.
func (p *NotifyPlugin) SendNotification(deviceIDs []string, n *sdk.Notification) error {
	var errs []error

	for _, id := range deviceIDs {
		dev, ok := p.store.Get(id)
		if !ok || !dev.Active {
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

// NotificationSettings implements sdk.NotifierInterface. It renders the
// backend-selector field followed by every registered backend's own
// device-config schema (each already Condition-gated to its own service id
// by convention, so only the selected backend's fields show in the UI).
func (p *NotifyPlugin) NotificationSettings() ([]sdk.JsonSchema, error) {
	backends := backend.All()

	ids := make([]string, 0, len(backends))
	for _, b := range backends {
		ids = append(ids, b.ID())
	}

	fields := []sdk.JsonSchema{
		{
			Type:     sdk.JsonSchemaTypeString,
			Key:      "service",
			Title:    "Service",
			Enum:     ids,
			Required: true,
		},
	}
	for _, b := range backends {
		fields = append(fields, b.Schema()...)
	}
	return fields, nil
}
