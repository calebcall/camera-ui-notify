# Changelog

All notable changes to **Notify** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.5.0] - 2026-07-25

### Fixed

- **Images now delivered when the publisher uses `ImageURL`** — the official (closed) NVR publishes the
  snapshot as a hosted `ImageURL` rather than inline `Thumbnail` bytes, so the inline-only backends
  (Telegram, Discord, Pushover) delivered text with no image. `SendNotification` now fetches an
  `ImageURL` once (when no inline `Thumbnail` is present) and attaches the bytes, so every backend
  renders the image regardless of which NVR published it. A fetch failure degrades gracefully: the
  `ImageURL` is left intact for URL-capable backends (ntfy/Gotify/webhook) and delivery is never
  aborted. Fetches are capped at 8 MiB.

### Added

- **Send-path logging** — `sendNotification` now logs the incoming notification (title, severity, which
  image fields are present), image resolution, and per-device delivery outcome via `Log`/`Success`/
  `Warn`/`Error` (visible without the debug flag), so the send flow is observable in normal operation.

## [0.4.2] - 2026-07-25

### Updated

- **README** - Updated README.md to fix broken lines and fix incorrect statements.

## [0.4.1] - 2026-07-25

### Changed

- Verification of the automated GitHub Actions publish pipeline (npm Trusted Publishing / OIDC). No functional changes since 0.4.0.

## [0.4.0] - 2026-07-25

### Added

- **Pushover backend** — hosted push to the Pushover app (app token + user key); sends the snapshot
  as an attachment and maps severity to Pushover priority (Info=0, higher=1).
- **Telegram backend** — delivery to a chat via a bot (bot token + chat ID); `sendPhoto` with the
  snapshot when present, else `sendMessage`, with an inline "Open camera" button for the deep link.
- **Discord backend** — delivery via a channel webhook; a severity-colored embed with the snapshot
  attached and the title linked to the deep link.

### Security

- **Redact secrets from transport errors.** Telegram's bot token (in the request URL path), a
  Discord webhook URL (itself a credential), and any secret embedded in a generic webhook URL are
  no longer leaked into logs when a delivery request fails — request URLs are stripped from
  `*url.Error` before wrapping.

## [0.3.0] - 2026-07-25

### Added

- **Inline thumbnail image (ntfy).** When a notification carries inline thumbnail bytes (e.g. the
  NVR's detection snapshot) and no image URL, the ntfy backend uploads it as a file attachment so
  the image renders in the notification.
- **Absolute deep links.** A new optional plugin config field, **camera.ui Base URL**, turns the
  publisher's relative deep link (e.g. `/cameras/<name>?startTs=…`) into an absolute URL so ntfy's
  tap-through (`Click`) works. Empty base URL leaves the link untouched.
- **Webhook thumbnail.** The generic webhook backend now includes the thumbnail as `thumbnailBase64`
  in its JSON payload when present.

### Fixed

- **Gotify visibility.** Severity now maps to Gotify priority 4–10 (Info=4 … Critical=10). Gotify
  treats priority 0–3 as silent (no system notification), so the previous 0–10 mapping made
  Info-severity notifications appear to deliver nothing.

### Notes

- Gotify cannot display an inline thumbnail image (it requires a hosted image URL, not raw bytes);
  images are delivered on ntfy only.

## [0.2.0] - 2026-07-24

### Changed

- **Config-driven single target replaces device registration.** The stock camera.ui UI has no
  way to *create* a notifier device — `registerDevice` is only ever called by the mobile app's
  push-registration flow, hardcoded to the official NVR's plugin name, and the generic
  notification settings panel renders read-only. The plugin's own settings page, however, does
  render an editable, savable form from any plugin's `StorageSchema`. Notify now holds exactly
  one target — a selected service (`ntfy`/`gotify`/`webhook`) plus that service's fields — in its
  own persisted config, set from the plugin's settings page in camera.ui rather than through a
  device-add flow.
- **`getDevices` synthesizes the device from config.** Instead of reading a stored device list, it
  reads the configured service and fields on every call, validates them (`ParseTarget`), and
  returns a single `sdk.NotifierDevice` (or none, if unconfigured/incomplete). `getDevice` mirrors
  this for the one synthesized id (`cfg:<service>`).
- **`registerDevice` / `updateDevice` / `revokeDevice` are now no-ops** (the first returns an
  error directing users to the plugin's settings page) — targets are configured, not registered.
  These remain only to satisfy the `Notifier` interface.
- **`notificationSettings()` returns nothing.** Target configuration now lives on the plugin's
  config tab; duplicating it in the notification "send test" panel would only confuse.

### Removed

- The device store (`src/store.go`) and its persisted `devices` key — replaced by the
  `StorageSchema`-based config described above.

Backends (ntfy, Gotify, generic webhook), their config fields, delivery format, and severity
mapping are unchanged from 0.1.0.

## [0.1.0] - 2026-07-24

Initial release — a fully-local, multi-backend camera.ui `Notifier` plugin.

### Added

- **Notifier plugin** — implements the full `Notifier` RPC surface (`getDevices`, `getDevice`,
  `registerDevice`, `updateDevice`, `revokeDevice`, `sendNotification`, `notificationSettings`)
  against a `Notifier`-only contract (`role: Hub`, `interfaces: [Notifier]`).
- **Pluggable-backend registry** — a `Backend` interface (id, label, config schema, target
  validation, delivery) with a package-level registry; each backend self-registers from its own
  `init()`, so adding a new backend later is one new file and a version bump.
- **Per-device backend selection** — each registered device is bound to one backend and holds
  that backend's validated config; the registration form is built dynamically from the union of
  every registered backend's schema, condition-gated on the selected service.
- **ntfy backend** — publishes to [ntfy.sh](https://ntfy.sh) or a self-hosted server (`server`,
  `topic`, optional `token`), mapping severity to ntfy's 1–5 priority scale.
- **Gotify backend** — publishes to a self-hosted [Gotify](https://gotify.net) server (`server`,
  `token`), mapping severity to Gotify's 0–10 priority scale.
- **Generic webhook backend** — delivers a JSON payload of the full notification to any HTTP
  endpoint (`url`, `method` [`POST`/`PUT`], optional custom header) for services without a
  dedicated backend.
- **Device persistence** — devices are stored as JSON under the plugin's own `DeviceStorage`,
  requiring no external database.

[0.2.0]: https://github.com/calebcall/camera-ui-notify/releases/tag/v0.2.0
[0.1.0]: https://github.com/calebcall/camera-ui-notify/releases/tag/v0.1.0
