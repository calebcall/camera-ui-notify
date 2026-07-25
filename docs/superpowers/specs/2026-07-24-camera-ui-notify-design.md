# camera.ui Notify — Multi-Backend Notifier Plugin (Design)

**Date:** 2026-07-24
**Status:** Approved design (pending spec review)

## Goal

A single, open-source, fully-local camera.ui **notifier** plugin that delivers
notifications to a user-selected service — **ntfy**, **Gotify**, or a **generic
webhook** in v1 — where adding a new service later is a code update (one new
file), never a new plugin.

## Background

camera.ui separates two notification roles:

- **Publishers** (capability `PublishNotifications`) emit events. Our
  open-source NVR plugin is a publisher — it calls `NotificationManager.Publish`
  for object-detection events.
- **Notifiers** (interface `Notifier`) own delivery *devices* and deliver
  notifications. The host's `NotificationManager.notify()` fans every
  notification out to **all** running plugins implementing `Notifier`.

There is intentionally **no** notifier in the open ecosystem today: the closed
NVR bundled a notifier that pushed to the camera.ui mobile app via camera.ui's
proprietary FCM/APNs cloud (license-gated). We cannot replicate push to *that*
app (no credentials, don't build the app). This plugin instead delivers to
services the user controls — the only fully-local path to real background push.

This plugin is a pure **notifier**. It is decoupled from the NVR: it delivers
notifications from *any* publisher (and system notifications), not just ours.

## Architecture

One plugin, `interfaces: [Notifier]`, with a pluggable-backend (strategy)
pattern:

- A `Backend` interface abstracts one delivery service.
- A package-level registry maps a backend's stable id → its implementation;
  each backend self-registers from its `init()`.
- The Notifier device model carries the per-device service selection: each
  registered *device* is one delivery target bound to one backend, holding that
  backend's config (e.g. ntfy server+topic). A user may register several devices
  across different backends and run them simultaneously.
- `sendNotification` dispatches each device to its backend's `Send`.

Adding a new service = a new `src/backend/<name>.go` implementing `Backend` +
`Register(...)` in its `init()`, plus a version bump. No new plugin, no core
change, no changes to `plugin.go`.

## Tech stack

- **Go** (matches the NVR plugin; same build/deploy toolchain).
- **camera.ui SDK (Go)** for the plugin runtime + `Notifier` RPC + storage.
- Delivery via the backends' plain HTTP APIs (Go stdlib `net/http`); no
  third-party client libraries required.
- Device persistence via the plugin's own `DeviceStorage` (a schema-registered
  key holding the device list as JSON) — the same pattern the NVR uses for its
  persistent `instanceId`. No SQLite: the device count is tiny.

## Global constraints

- Package name: `@calebcall/camera-ui-notify` (own repo, not published to a
  reserved namespace). `displayName: "Notify"`.
- Contract: `role: Hub`, `interfaces: [Notifier]` ONLY — **no** `NVR`, **no**
  `OAuthCapable`, **no** `PublishNotifications` capability. (This plugin does
  not publish and is not an NVR; declaring interfaces it doesn't implement is
  exactly the bug we removed from the NVR — see that plugin's contract note.)
- Fully local: no camera.ui cloud, no license check, no outbound calls except
  to the user-configured backend endpoints.
- Commit hygiene (repo convention): no `Co-Authored-By`, no "Claude"/"Generated
  with" lines in commits.
- Every backend's `Send` is best-effort and self-contained: a failure is
  returned to the host (which logs it) and never aborts delivery to other
  devices/backends.

## Components

### 1. Contract (`contract.ts`)

```ts
{
  name: 'Notify',
  role: PluginRole.Hub,
  provides: [],
  consumes: [],
  interfaces: [PluginInterface.Notifier],
  capabilities: [],
}
```

### 2. `Backend` interface + registry (`src/backend/backend.go`)

```go
type Target struct {
    Backend string            // backend id, e.g. "ntfy"
    Config  map[string]string // validated, backend-specific (server, topic, token, url, …)
}

type Backend interface {
    ID() string        // stable id: "ntfy" | "gotify" | "webhook"
    Label() string     // human label for the service dropdown
    // Schema returns this backend's device-config fields. Each field's
    // Condition gates it to this backend (service == ID()), so the UI shows
    // only the selected backend's fields.
    Schema() []sdk.JsonSchema
    // ParseTarget validates raw registration input into a Target.Config,
    // returning an error for missing/invalid fields (surfaced to the user).
    ParseTarget(input map[string]any) (map[string]string, error)
    // Send delivers n to the target. Best-effort; returns an error on failure.
    Send(ctx context.Context, cfg map[string]string, n *sdk.Notification) error
}

var registry = map[string]Backend{}
func Register(b Backend)            // called from each backend's init()
func Get(id string) (Backend, bool)
func All() []Backend                // registry snapshot, stable order, for schema/enum
```

### 3. Device model + registration (`src/plugin.go`)

A device (`sdk.NotifierDevice`) carries `metadata` = `{ service: <id>, ...cfg }`.

- `notificationSettings()` → `service` enum (from `backend.All()`) + the union
  of every backend's `Schema()` fields, each `Condition`-gated to its service.
- `registerDevice(ownerUserId, input)`:
  1. read `input["service"]`; look up the backend (error if unknown),
  2. `backend.ParseTarget(input)` → validated config,
  3. create a device `{id: uuid, ownerUserId, name, active: true, metadata:{service, ...cfg}}`,
  4. persist to the device store, return it.
- `getDevices(ownerUserIds)` / `getDevice(id)` / `updateDevice(id, patch)`
  (name/active) / `revokeDevice(id)` operate on the store.
- `sendNotification(deviceIds, n)`: for each device → resolve backend by
  `metadata.service` → `backend.Send(ctx, cfg, n)`; collect per-device errors,
  never abort the batch.

### 4. Device store (`src/store.go`)

A small persistence layer over the plugin's `DeviceStorage`:
- Declares a storage schema with one hidden key `devices` (JSON array), so
  `SetValue`/`GetValue` persist (the NVR's `instanceId` gotcha: values don't
  persist without a registered schema).
- CRUD helpers guarded by a mutex; devices serialized as JSON.

### 5. Backends (v1)

Severity mapping (shared intent): `Info`→normal, `Warn`→high, `Error`→high,
`Critical`→max/urgent + bypass DND where the backend supports it.

- **ntfy** (`src/backend/ntfy.go`)
  - Config: `server` (base URL, default `https://ntfy.sh`), `topic` (required),
    optional `token` (Bearer) for protected/self-hosted.
  - Send: `POST {server}/{topic}` with headers `Title`, `Priority`
    (1..5 from severity), `Tags`, `Click` (= `deepLink`), `Attach`/`Icon`
    (= `imageUrl`); body = notification body. Auth header when token set.
- **Gotify** (`src/backend/gotify.go`)
  - Config: `server` (base URL, required), `token` (app token, required).
  - Send: `POST {server}/message?token={token}` JSON `{title, message,
    priority}` (0..10 from severity); `extras` for click URL
    (`client::notification.click` → `deepLink`) and image where applicable.
- **Generic webhook** (`src/backend/webhook.go`)
  - Config: `url` (required), optional `method` (default POST), optional
    `headerName`/`headerValue` for a single custom auth header.
  - Send: HTTP `{method} {url}` with `Content-Type: application/json` and a
    stable JSON body of the full notification (`title, subtitle, body,
    severity, tag, imageUrl, deepLink, data, createdAt`). Maximum flexibility
    for anything without a dedicated backend.

### 6. Packaging / build / deploy

- `package.json` (`@calebcall/camera-ui-notify`, `main: ./main.go`), `go.mod`,
  `cameraui.config.ts`, `LICENSE.md` (MIT), `README.md`, `CHANGELOG.md`,
  `.gitignore` (dist/bundle/node_modules — build artifacts NOT committed).
- Build/deploy mirrors the NVR: `npm run bundle:dev` → `contract.cjs`;
  `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o bundle/dist/bin/plugin
  ./src/`; install into `plugins/@calebcall/camera-ui-notify/`, restart, enable.
  (Its own real name/namespace — unlike the NVR, no hardcoded-slot requirement,
  since the host discovers notifiers by the `Notifier` interface, not a fixed
  package id.)

## Testing

- `backend` package: table-driven tests per backend — `ParseTarget` validation
  (required/missing/invalid), and `Send` against an `httptest.Server` asserting
  method, path, headers, and body for representative severities + an event with
  `imageUrl`/`deepLink`. Registry: `Register`/`Get`/`All` (stable order, unknown
  id).
- `plugin` package: `registerDevice` (valid → stored; unknown service →
  error), `getDevices`/`getDevice` filtering by owner, `updateDevice`
  (name/active), `revokeDevice`, and `sendNotification` dispatch (routes each
  device to the right backend; one backend failing doesn't abort the batch) —
  using a fake in-memory `DeviceStorage` and a fake backend registered for the
  test.
- `contract` test: asserts `interfaces: [Notifier]` and no `NVR`/`publish`.

## Out of scope (v1)

- Push to the camera.ui mobile app (impossible locally — see Background).
- A "test notification" button, per-severity routing rules, message templating,
  retry/queueing. Backends beyond the three above (they're just later updates).

## Open questions

None blocking. Backend field schemas will use the SDK `JsonSchema.Condition`
mechanism (confirmed present) for the service-conditional form; exact
`SchemaCondition` shape to be verified during scaffolding.
