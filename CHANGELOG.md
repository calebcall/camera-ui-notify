# Changelog

All notable changes to **Notify** are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.7.1] - 2026-08-02

### Fixed

- **Alertmanager mode: documented the required path prefix.** Mimir and Grafana Cloud serve the
  Alertmanager API under a prefix (`/alertmanager` by default); a standalone Alertmanager serves it
  at the root. 0.7.0's field placeholder and README both showed a bare host, so configuring it the
  documented way produced `404 page not found` on every send. The README now gives both forms side
  by side, and the field description and placeholder show the Grafana Cloud shape. No code was
  wrong here — the documentation was.
- **A 404 from Alertmanager now explains itself.** Go's default mux answers with a bare
  `404 page not found`, which says nothing about the missing prefix that caused it. The error now
  adds that hint. Other statuses are untouched.
- **A pasted full endpoint is accepted.** Alertmanager's own docs show the complete
  `.../api/v2/alerts` URL, so copying it into the base-URL field is the obvious mistake; a trailing
  `/api/v2/alerts` is now trimmed rather than producing `/api/v2/alerts/api/v2/alerts`.
- **Documented where Grafana Cloud Alertmanager credentials come from.** The username is the
  numeric Alertmanager instance ID from the Cloud portal; the password is an Access Policy token
  (`glc_...`) with the `alerts:write` scope — *not* a Grafana service-account token (`glsa_...`),
  which authenticates to Grafana rather than to the Alertmanager.

### Removed

- **`grafana_irm_ttl` and the IRM `endsAt` experiment, both added in 0.7.0.** They did nothing. IRM
  decides whether a group is resolved from a template on the payload's status — its default is
  `{{ payload.status == "resolved" }}` — and ignores `endsAt` entirely, so no value in a single
  firing request can close a group; only a second request can. IRM alerts once again carry the
  documented never-resolves sentinel `0001-01-01T00:00:00Z`, which states the actual behaviour
  instead of implying an auto-close that never happens, and the README says plainly that groups are
  closed by hand.

  A delayed resolve was considered and rejected: it would require a background timer and per-event
  state in a plugin that is otherwise one stateless POST per event, and a restart would strand the
  group open regardless.

## [0.7.0] - 2026-08-02

### Changed

- **The Grafana `alerts` mode is renamed `alertmanager` and now posts to an Alertmanager directly**
  ([#33](https://github.com/calebcall/camera-ui-notify/issues/33)). **This mode requires
  reconfiguration.** It never worked in 0.6.0 or 0.6.1 — every send failed with
  `400 bad request data` — so no working setup is disturbed.

  The mode targeted `{grafanaServer}/api/alertmanager/grafana/api/v2/alerts` on the assumption that
  Grafana's built-in Alertmanager accepts injected alerts. It does not. Grafana's route table
  declares built-in-Alertmanager operations with a literal `grafana` path segment and external ones
  with `{DatasourceUID}`; there is a `GET /alertmanager/grafana/api/v2/alerts` but **no `POST`
  equivalent** — reads are supported, writes are not. The forking handler
  (`pkg/services/ngalert/api/forking_alertmanager.go`) resolves an Alertmanager *datasource* by UID
  and always returns the external proxy, never the built-in service, so a request naming `grafana`
  as the UID matches no datasource and fails. Grafana-managed alerts can only originate from
  Grafana's own rule evaluation; there is no supported path to inject one.

  The mode now addresses an Alertmanager's own v2 API — `POST {alertmanager}/api/v2/alerts` — with
  optional basic auth. That works with a standalone Prometheus Alertmanager, Mimir/Cortex, and
  Grafana Cloud's hosted Alertmanager (username = instance ID, password = API token). The name
  follows the behaviour: this mode talks to Alertmanager, not to Grafana.

  Config changes for this mode: `grafana_server` and `grafana_token` are no longer used (they are
  now annotations-only) and are replaced by `grafana_am_url` plus the optional `grafana_am_user` /
  `grafana_am_password` pair. Setting only one of the two credential fields is rejected at parse
  time rather than surfacing as a confusing 401. `grafana_alertname` and `grafana_ttl` are
  unchanged, and the alert payload itself is unchanged — it already matched Alertmanager's
  documented `postableAlert` schema; only the destination was wrong.

- **`grafana_server` / `grafana_token` are now gated to annotations mode only**, since it is the
  one mode that addresses a Grafana instance. They no longer use the `in` condition operator.

### Fixed

- **Grafana modes now label the camera with its display name instead of its UUID**
  ([#34](https://github.com/calebcall/camera-ui-notify/issues/34)). `Data["cameraId"]` is a UUID for
  the publishers seen so far, so alerts read `camera=07614b1d-d5de-48b7-bbb2-592a64a97ead` and IRM
  grouped under `camera.ui:07614b1d-…`, which is unusable at a glance.

  The name is resolved cheapest-first: `Data["cameraName"]` if a publisher supplies one, else the
  camera segment of the deep link (camera.ui routes cameras by display name, so this is `Patio`
  while the id is a UUID), else the raw id so a notification with neither still gets a label. No
  RPC and no camera lookup — `DeviceManager.GetCamera` would also resolve a name but builds a full
  camera-device proxy and calls `init()` on it, which is heavy and side-effecting for a Hub plugin
  that owns no cameras.

  This reaches the annotations `camera:<name>` tag, the alertmanager `camera` label, and the IRM
  `camera` label, `groupLabels` and `groupKey`. Alertmanager and IRM modes additionally keep the
  raw id as `camera_id` (omitted when it would merely repeat `camera`), since names change and ids
  do not — routing rules that must survive a rename can match on that.

  **IRM alert groups will regroup**: existing groups keyed `camera.ui:<uuid>` are replaced by
  `camera.ui:<name>`.

- **`grafana_ttl` no longer sets a `Step`.** With `min=30 step=30`, an HTML5 number input rejects
  legitimate values such as 100 or 450. Nothing about a TTL needs 30-second granularity.

### Added

- **`grafana_irm_ttl`** (default `300`, minimum `30`). IRM alerts now carry a future `endsAt`
  (`startsAt + grafana_irm_ttl`) instead of the never-resolves sentinel `0001-01-01T00:00:00Z`.
  That is how Alertmanager expresses "resolves on its own at this time", and IRM's
  `grafana_alerting` templates read the same envelope — but whether IRM actually acts on it is
  **unverified**. If it does not, groups stay open until closed by hand, exactly as before, so this
  is an improvement-or-no-change rather than a risk. Still one stateless POST per event: there is
  no follow-up `state: "ok"` request and no background timer.

## [0.6.1] - 2026-08-02

### Fixed

- **Grafana IRM mode now renders correctly on a `grafana_alerting` integration** ([#31](https://github.com/calebcall/camera-ui-notify/issues/31)).
  0.6.0 sent only Grafana OnCall's formatted-webhook field set. IRM renders each alert group
  through Jinja2 templates chosen by the integration's *type*, and a `grafana_alerting`
  integration's templates read `payload.status` and `payload.alerts[]` — neither of which we sent.
  The result was an alert group that arrived intact but displayed as
  `Status: Unknown ⚠️ (Template Warning: 'dict object' has no attribute 'alerts')`, with
  `numFiring`/`numResolved` showing IRM's zero defaults for the absent `alerts` array.

  The payload is now a union of both shapes: Grafana Alerting's documented webhook envelope
  (`receiver`, `status`, `alerts[]` carrying labels, annotations, `startsAt`/`endsAt`,
  `generatorURL`, `fingerprint` and `imageURL`, plus `groupLabels`, `commonLabels`,
  `commonAnnotations`, `externalURL`, `version`, `groupKey`, `truncatedAlerts`) alongside the
  formatted-webhook fields 0.6.0 already sent. `title`, `message` and `state` are read by both and
  are unchanged, so a **Webhook**-type integration configured against 0.6.0 keeps working exactly
  as before — this is additive, not a replacement.

  This was a design-time error rather than an implementation one: the spec specified the wrong
  payload shape, and the unit tests could not catch it because they assert our body against a test
  server that accepts anything. The envelope now follows the schema Grafana documents for its
  webhook contact point.

### Added

- **Per-camera grouping for IRM alert groups.** `groupKey` is `camera.ui:<cameraId>`, falling back
  to `camera.ui` when a notification names no camera, so a busy camera cannot bury a quiet one.
  Each event keeps a distinct `fingerprint` within its group. IRM groups still do not auto-resolve
  — that remains deliberate, since a follow-up `state: "ok"` would need a background timer.
- **The snapshot now reaches IRM through the documented `imageURL` alert field** as well as
  `image_url`, so it renders under either integration type. Still only when the publisher supplied
  a hosted `ImageURL`.
- **`externalURL`** is derived from the absolute deep link's scheme and host, so it needs no new
  configuration; it is omitted when `base_url` is unset and the deep link is therefore relative.

## [0.6.0] - 2026-08-01

### Added

- **Grafana backend** with three delivery modes selected by a **Mode** field:
  - **Annotations** — a point-in-time, organization-wide annotation via `POST /api/annotations`,
    tagged `camera.ui` / `camera:<id>` / `severity:<level>` plus any extra tags, so dashboards can
    surface camera events through a tag-filtered annotation query.
  - **Alerts** — a firing alert via `POST /api/alertmanager/grafana/api/v2/alerts`, routed by your
    existing notification policies. `endsAt` is `startsAt + grafana_ttl` (default 300s) so Grafana
    auto-resolves it with no second request, and a unique `event_id` label keeps Alertmanager from
    deduplicating two detections on the same camera into one alert.
  - **IRM** — one alert group per event via a Grafana IRM / OnCall inbound webhook, using the
    formatted-webhook field set. The only Grafana mode that renders the snapshot, and only when the
    publisher supplied a hosted `ImageURL`.

  The integration URL for IRM embeds its own token, so it is a masked field and is stripped from
  every error message, including transport failures.

## [0.5.6] - 2026-08-01

### Changed

- **`@camera.ui/cli` `~0.0.75`** (from `~0.0.74`) — picks up the fix for the bug this project reported
  upstream as [cameraui/cli#31](https://github.com/cameraui/cli/issues/31) (tracked here as #16):
  `cui bundle` was writing an `optionalDependencies` block into the plugin repo's own `package.json` on
  every run, pinned to a version that does not exist on npm until the release publishes it — so
  `package-lock.json` could never resolve it and `npm ci` failed, breaking the release workflow at its
  "Install dependencies" step. 0.0.75 stops touching the repo's `package.json` entirely, which also
  removes the merge-vs-replace inconsistency and the whole-file reformatting reported alongside it.
  Verified: `package.json` is byte-identical before and after `npm run bundle`, and the published bundle
  still carries all 8 platform `optionalDependencies` at the right version.
- **`PUBLISHING.md`** — replaced the "Never commit the `optionalDependencies` block" workaround with a
  short explanation of where that block legitimately comes from (`bundle/package.json`, regenerated from
  the Go targets each run) and why it must not appear in the repo's own `package.json`, noting the CLI
  versions affected and that 0.0.75 fixes it.

No change to the plugin itself — this release is build tooling and documentation only, and the published
artifact is functionally identical to 0.5.5. Every other dependency was already current: `@camera.ui/sdk`
1.2.3, `github.com/cameraui/sdk/go` v1.2.6, `github.com/cameraui/rpc/go` v1.0.7, camera.ui floor 2.0.24.

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
