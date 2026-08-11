package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"

	"github.com/calebcall/camera-ui-notify/src/backend"
)

// imageFetchClient fetches a notification's hosted ImageURL when a backend
// needs inline bytes (see resolveThumbnail). Its timeout is deliberately short:
// a slow snapshot host must never stall the whole notification fan-out.
// Package-level so tests can point it at an httptest server.
var imageFetchClient = &http.Client{Timeout: 10 * time.Second}

// maxThumbnailBytes caps a fetched ImageURL so a hostile or oversized image
// can't exhaust memory. 8 MiB comfortably covers any camera snapshot.
const maxThumbnailBytes = 8 << 20

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

	p.logf("notify: sendNotification for %d device(s): title=%q severity=%q hasThumbnail=%t imageUrl=%t deepLink=%t tag=%q silent=%t",
		len(deviceIDs), n.Title, n.Severity, len(n.Thumbnail) > 0, n.ImageURL != "", n.DeepLink != "", n.Tag, n.Silent)

	// camera.ui republishes a notification under the same Tag when it has
	// more to say about an event it already announced — in practice, the AI
	// description landing a few seconds after the detection alert. That
	// republish carries Silent. Backends that can edit the original message
	// (Telegram, Discord) always get it, because replacing costs the user
	// nothing; the rest deliver it quietly, or drop it when the user asked
	// for exactly one notification per event.
	skipSilent := backend.SilentDelivery(*n) && p.silentUpdatePolicy() == silentUpdatesSkip

	// base_url is plugin-level config (not per-backend), so it's read once
	// here rather than threaded into each backend's cfg map. When set, and
	// the notification's DeepLink is router-relative (as published by
	// camera.ui itself), dispatch a copy with an absolute DeepLink so
	// backends whose tap-through target requires a fully-qualified URL
	// (e.g. ntfy's Click header) work correctly. The caller's *n is never
	// mutated: every device sees either the original notification or an
	// independent copy.
	toSend := *n
	baseURL, _ := p.Storage.GetValue("base_url", "").(string)
	if baseURL != "" && strings.HasPrefix(n.DeepLink, "/") {
		toSend.DeepLink = strings.TrimRight(baseURL, "/") + n.DeepLink
	}

	// Some publishers (notably the closed NVR) attach the snapshot as a hosted
	// ImageURL and no inline Thumbnail. Backends that can only send inline
	// bytes (Telegram/Discord/Pushover) would otherwise deliver text only.
	// Resolve once on the copy so *n is never mutated and the fetch is shared
	// across every device in this fan-out.
	p.resolveThumbnail(context.Background(), &toSend)

	dispatched := 0
	for _, id := range deviceIDs {
		dev, err := p.GetDevice(id)
		if err != nil || dev == nil || !dev.Active {
			p.logf("notify: device %s not deliverable (unknown, inactive, or not ours), skipping", id)
			continue
		}

		service, _ := dev.Metadata["service"].(string)
		b, ok := backend.Get(service)
		if !ok {
			p.warnf("notify: device %s references unknown service %q, skipping", id, service)
			continue
		}

		// With no tag there is nothing to replace, so even a replacing
		// backend would have to post a second message.
		replaceable := n.Tag != "" && backend.ReplacesTaggedMessages(b)
		if skipSilent && !replaceable {
			p.logf("notify: device %s (%s) cannot replace by tag, skipping silent update %q", id, service, n.Title)
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

		p.logf("notify: dispatching to device %s via %q", id, service)
		if err := b.Send(context.Background(), cfg, toSend); err != nil {
			wrapped := fmt.Errorf("device %s (%s): %w", id, service, err)
			p.errorf("notify: send failed: %v", wrapped)
			errs = append(errs, wrapped)
			continue
		}
		dispatched++
		p.successf("notify: delivered to device %s via %q", id, service)
	}

	p.logf("notify: sendNotification complete: %d delivered, %d failed", dispatched, len(errs))
	return errors.Join(errs...)
}

// resolveThumbnail ensures the notification carries inline image bytes when the
// publisher supplied only a hosted ImageURL. The closed NVR sets ImageURL and
// no inline Thumbnail (the SDK's stated preference), but Telegram/Discord/
// Pushover can only attach inline bytes — without this they'd deliver text
// only. When a Thumbnail is already present, or no ImageURL was given, this is
// a no-op. A fetch failure is logged and swallowed: ImageURL is left intact so
// URL-capable backends (ntfy/Gotify/webhook) still render it, and the others
// degrade to text — delivery is never aborted for a missing image.
func (p *NotifyPlugin) resolveThumbnail(ctx context.Context, n *sdk.Notification) {
	if len(n.Thumbnail) > 0 || n.ImageURL == "" {
		return
	}
	data, err := fetchImage(ctx, imageFetchClient, n.ImageURL)
	if err != nil {
		p.warnf("notify: could not fetch ImageURL for inline attachment (delivering text/URL only): %v", backend.RedactRequestError(err))
		return
	}
	p.logf("notify: fetched %d image bytes from ImageURL for inline attachment", len(data))
	n.Thumbnail = data
}

// fetchImage GETs url and returns the response body, capped at
// maxThumbnailBytes. A non-2xx status or an empty body is an error. Kept
// separate from resolveThumbnail so it is unit-testable against an httptest
// server.
func fetchImage(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("image fetch: server responded %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailBytes))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("image fetch: empty response body")
	}
	return data, nil
}

// logf / successf / warnf / errorf are nil-safe, Sprintf-style wrappers around
// the plugin logger so the send path can log without repeating the p.Logger !=
// nil guard at every call site. Log/Success/Warn/Error are used (not Debug,
// which is suppressed unless the debug flag is set) so the send flow is visible
// in normal operation.
func (p *NotifyPlugin) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Log(fmt.Sprintf(format, args...))
	}
}

func (p *NotifyPlugin) successf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Success(fmt.Sprintf(format, args...))
	}
}

func (p *NotifyPlugin) warnf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Warn(fmt.Sprintf(format, args...))
	}
}

func (p *NotifyPlugin) errorf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Error(fmt.Sprintf(format, args...))
	}
}

// silentUpdatePolicy reads the persisted "silent_updates" selection,
// defaulting to silentUpdatesDeliver for an unset or unrecognized value —
// dropping notifications is the destructive choice, so it is never the
// fallback.
func (p *NotifyPlugin) silentUpdatePolicy() string {
	policy, _ := p.Storage.GetValue("silent_updates", silentUpdatesDeliver).(string)
	if policy == silentUpdatesSkip {
		return silentUpdatesSkip
	}
	return silentUpdatesDeliver
}

// NotificationSettings implements sdk.NotifierInterface. The service
// selection and its config now live on the plugin's config tab
// (StorageSchema); duplicating that form in the "send test" panel would
// only confuse. The panel still lists the synthesized device as a target
// via GetDevices.
func (p *NotifyPlugin) NotificationSettings() ([]sdk.JsonSchema, error) {
	return nil, nil
}
