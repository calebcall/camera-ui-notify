# Changelog

All notable changes to **Notify** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.5.5] - 2026-08-01

### Changed

- **Go SDK pinned to `v1.2.6`** (from `v1.2.4`) — routine catch-up on two upstream patches. No source
  change required; `go build`, `go test` and the full 8-target bundle all pass untouched.
- **`@camera.ui/cli` `~0.0.74`** (from `~0.0.73`) — routine. Still writes an `optionalDependencies` block
  into the repo's own `package.json` on every bundle, so the `PUBLISHING.md` guidance from #16 continues
  to apply: discard that hunk, never commit it.
- **Minimum camera.ui raised to `2.0.24`** (from `2.0.23`) — 2.0.24 carries three host-side fixes that
  directly affect plugin settings: panels getting permanently stuck on "No configuration available" when
  a settings request was superseded, a camera's plugin toggle deleting that plugin's stored per-camera
  settings, and cleared settings becoming `undefined` instead of falling back to their default. The
  plugin wire protocol is unchanged from 2.0.23 (`protocolLevel` is still 1), so this floor is about
  guaranteeing Notify's own configuration behaves, not about protocol compatibility.
- **`typescript` `7.0.2`** (from `5.9.3`) — moves off the 5.x line onto the native compiler. Safe here
  because npm nests `typescript@5.9.3` under `@camera.ui/cli`'s copy of `ts-import`, which still declares
  `peerDependencies: { typescript: "5" }`; the CLI's `cameraui.config.ts` loader therefore keeps running
  on TS 5 while the repo's own pin moves to 7. Verified: `cui bundle` still bundles and validates the
  contract, and `contract.ts`/`cameraui.config.ts` type-check clean under 7.0.2. Two consequences worth
  knowing: `node_modules` now holds two TypeScript installs, and because TS 7 ships as a native binary
  the lockfile gains 20 optional `@typescript/typescript-<platform>` packages (only the host's own is
  installed). Nothing in the build consumes the root pin — there is no `tsconfig.json` and no `tsc` in
  any script — so it serves ad-hoc type-checking only.

### Fixed

- **`package-lock.json` now records the correct version** — it still said `0.5.3` through the 0.5.4
  release because the lockfile was last written before that version bump. Harmless (npm does not publish
  from it) but misleading; regenerated here.

## [0.5.4] - 2026-07-31

### Changed

- **Go SDK pinned to `v1.2.4`** (from `v1.1.18`) — this is the SDK half of camera.ui's standalone-sensor
  refactor, released 30 minutes before `server-v2.0.23`. The diff rewrites `camera_device.go`, the whole
  `sensor_*` family, `plugin_contract.go`, `plugin_notifier.go` and `plugin_interfaces.go`, and adds
  `manager_sensor.go` and `observable.go`. Notify required **no source change**: it declares
  `provides: []`/`consumes: []`, owns no cameras, and touches none of the camera-bound sensor
  registration functions the refactor removed. `NotifierInterface`, `Notification` and `NotifierDevice`
  remain compatible, and the detection-event types used by the diagnostics path still resolve.
- **Minimum camera.ui raised to `2.0.23`** (from `2.0.15`) — `server-v2.0.23` is the release that landed
  the new sensor system and, per its own notes, stops honouring plugins built against the old one. Since
  this version of Notify ships an SDK v1.2.4 binary, a pre-2.0.23 server would be pairing a new plugin
  wire protocol with a host that predates it. The server enforces this floor through
  `checkEngineCompatibility` at install time, so affected users simply stay on 0.5.3 instead of
  installing a build their host can't speak.
- **`@camera.ui/cli` `~0.0.73`** (from `~0.0.65`) — the CLI now emits one npm package per platform target
  under `bundle/platforms/` and records them as `optionalDependencies` in the published bundle. npm
  installs only the entry matching the host's `os`/`cpu`/`libc` and the server executes that binary in
  place, so nothing depends on an install-time lifecycle script — which npm v12 disables for
  dependencies by default. `cui publish` publishes each platform package before the root one and keeps
  the versions in lockstep. The set of published package names is unchanged, so the Trusted Publishing
  setup in `PUBLISHING.md` still applies as written. Note that `npm run bundle` also writes that block
  into the repo's own `package.json`; it must not be committed (it would break `npm ci`), see #16 and
  the new section in `PUBLISHING.md`.
- **`@camera.ui/sdk` `~1.2.3`** (from `~0.0.22`) — required, not optional: `@camera.ui/cli@0.0.73`
  itself depends on `@camera.ui/sdk@~1.2.3`. Despite the major-version jump this is a no-op for
  `contract.ts`; `PluginRole.Hub` (`'hub'`) and `PluginInterface.Notifier` (`'Notifier'`) keep their
  identifiers and values, and the contract still type-checks and passes `cui bundle`'s validation.
- **`@types/node` `^26.1.2`**, **`updates` `^17.20.2`** — routine. `cross-env` and `rimraf` were already
  current.
- **Transitive Go dependencies refreshed** — `github.com/cameraui/rpc/go` v1.0.7, `klauspost/compress`
  v1.19.1, `nats-io/nkeys` v0.4.16, `golang.org/x/crypto` v0.54.0, `golang.org/x/sys` v0.47.0.

### Notes

- **`typescript` stays pinned at `5.9.3`** even though 7.0.2 is published. `@camera.ui/cli` loads
  `cameraui.config.ts` through `ts-import@5.0.0-beta.1`, which declares `peerDependencies:
  { typescript: "5" }`. 5.9.3 is the newest 5.x, so the existing exact pin is already correct and
  deliberate; moving to 6 or 7 would break `cui bundle`.
- Verified end to end: `go build ./src/...`, `go test ./...` (both packages pass), and `npm run bundle`
  cross-compiling all 8 platform targets with contract validation. `staticcheck` (43 findings) and
  `golangci-lint` (9 findings) return byte-identical counts before and after the upgrade — all
  pre-existing, tracked in #14.

## [0.5.3] - 2026-07-29

### Changed

- **Go SDK pinned to `v1.1.18`** (from `v1.1.11`) — routine catch-up ahead of camera.ui's upcoming sensor
  refactor, which makes sensors standalone devices and deprecates the camera-bound sensor registration
  functions (`CameraDevice.AddSensor`/`GetSensor`/`GetSensors`/`GetSensorsByType`). Notify calls none of
  those and declares `provides: []`/`consumes: []`, so no source change was required; catching up now
  keeps any future breakage attributable to one change rather than to seven versions plus a refactor.
  No behaviour change — the `NotifierInterface`, `Notification` and `NotifierDevice` types are identical
  between the two SDK versions.

## [0.5.2] - 2026-07-26

### Fixed

- **Deep-link button no longer says "Open camera" on non-detection notifications** — the tap-through
  label was hardcoded and applied to any absolute deep link, so plugin-update, system-update and other
  operational notifications rendered a correct link under an incorrect label. The label is now derived
  from the link's destination: "Open camera" when it opens a specific camera's page, "Open in camera.ui"
  otherwise. Affects Telegram (inline-keyboard button) and Pushover (`url_title`); ntfy, Gotify, Discord
  and the generic webhook carry no label text and were never affected.

## [0.5.1] - 2026-07-25

### Fixed

- **gofmt** — corrected struct-literal field alignment in `src/backend/gotify.go` (an inline comment
  inside the payload literal had thrown off `gofmt`). No functional change.

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
