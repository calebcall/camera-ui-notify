# Changelog

All notable changes to **Notify** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.1.0]: https://github.com/calebcall/camera-ui-notify/releases/tag/v0.1.0
