# Changelog

All notable changes to **Notify** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
