<h1 align="center">Notify</h1>

<p align="center">
  A fully-local, multi-backend <a href="https://github.com/seydx/camera.ui">camera.ui</a> notifier
  plugin — delivers notifications to a service you control (<a href="https://ntfy.sh">ntfy</a>,
  <a href="https://gotify.net">Gotify</a>, or a generic webhook), entirely on your own hardware,
  with no cloud dependency and no license check.
</p>

---

## Why this exists

camera.ui separates two notification roles:

- **Publishers** (capability `PublishNotifications`) emit events. Our open-source NVR plugin
  (`@calebcall/camera-ui-nvr-local`) is a publisher — it calls `NotificationManager.Publish` for
  object-detection events.
- **Notifiers** (interface `Notifier`) own delivery *devices* and actually deliver notifications.
  The host's `NotificationManager.notify()` fans every notification out to **all** running plugins
  implementing `Notifier`.

There is intentionally no notifier in the open ecosystem: the closed, official NVR bundled a
notifier that pushed to the camera.ui mobile app through camera.ui's proprietary FCM/APNs cloud
relay — a license-gated path we neither have credentials for nor can replicate. This plugin takes
the only fully-local road to real background push instead: delivering to a push/webhook service
*you* run or control.

This plugin is a pure **notifier**. It is decoupled from any single publisher — it delivers
notifications from **any** publisher, including our NVR plugin's detection events and camera.ui's
own system notifications, not just one plugin's output.

## How it works

One plugin, contract `interfaces: [Notifier]`, built around a pluggable-backend (strategy) pattern:

- A `Backend` interface abstracts one delivery service (id, label, config schema, target
  validation, and delivery).
- A package-level registry maps each backend's stable id to its implementation; every backend
  self-registers from its own `init()`.
- Each registered **device** (`sdk.NotifierDevice`) is one delivery target bound to one backend,
  holding that backend's validated config in its metadata. Register as many devices as you like,
  across the same or different backends, and they all fire in parallel.
- `sendNotification` dispatches every targeted device to its backend's `Send`. A single backend
  failure is logged and returned to the host, but never aborts delivery to the others.

Adding a new backend later is **one new file** — `src/backend/<name>.go` implementing `Backend`
plus `Register(...)` in its `init()` — and a version bump. No new plugin, no core change, no
changes to `plugin.go`.

## Backends (v1)

Severity is mapped consistently across backends via `backend.PriorityScale`, which spreads
camera.ui's four severity levels (`info` → `warn` → `error` → `critical`) evenly across each
backend's native priority range, `info` at the low end and `critical` at the high end.

### ntfy

Publishes to [ntfy.sh](https://ntfy.sh) or a self-hosted ntfy server.

| Field    | Required | Default              | Notes                                              |
| -------- | -------- | --------------------- | --------------------------------------------------- |
| `server` | no       | `https://ntfy.sh`     | Base URL of the ntfy server. Trailing `/` trimmed. |
| `topic`  | yes      | —                     | The ntfy topic to publish to.                      |
| `token`  | no       | —                     | Access token for a protected/self-hosted topic, sent as `Authorization: Bearer <token>`. |

Delivery: `POST {server}/{topic}` with the notification body as the request body, plus `Title`,
`Priority` (1–5, from severity), `Click` (deep link, if set), and `Attach`/`Icon` (image URL, if
set) headers.

### Gotify

Publishes to a self-hosted [Gotify](https://gotify.net) server.

| Field    | Required | Notes                                          |
| -------- | -------- | ----------------------------------------------- |
| `server` | yes      | Base URL of the Gotify server. Trailing `/` trimmed. |
| `token`  | yes      | Gotify application token, used to authenticate published messages. |

Delivery: `POST {server}/message?token={token}` with a JSON body `{title, message, priority}`
(priority 0–10, from severity), plus a `client::notification.click` extra for the deep link and a
`bigImageUrl` extra when an image URL is set.

### Generic webhook

Delivers to any HTTP endpoint you provide — the fallback for anything without a dedicated backend.

| Field         | Required | Default | Notes                                                         |
| ------------- | -------- | ------- | -------------------------------------------------------------- |
| `url`         | yes      | —       | Endpoint that receives the notification.                       |
| `method`      | no       | `POST`  | `POST` or `PUT`.                                                |
| `headerName`  | no       | —       | Optional custom header name (e.g. for a shared secret). Requires `headerValue` if set. |
| `headerValue` | no       | —       | Value of the custom header. Requires `headerName` if set.       |

Delivery: `{method} {url}` with `Content-Type: application/json` and (if configured) the custom
header, carrying a JSON body of `{title, subtitle, body, severity, tag, imageUrl, deepLink, data,
createdAt}`.

## Setting up a device

From the camera.ui **notifications/devices** UI:

1. Add a new notification device and pick a **Service** (`ntfy`, `Gotify`, or `Generic webhook`)
   from the dropdown built from the registered backends.
2. Fill in that service's fields (only the selected service's fields are shown — the rest are
   condition-gated out).
3. Save. The device is validated (`ParseTarget`) and persisted; notifications from any publisher
   are now delivered to it until it's deactivated or revoked.

You can register multiple devices — e.g. an ntfy topic for yourself and a Gotify server for a
household — and every active device receives every notification independently.

## Tech stack

- **Go** (matches the NVR plugin; same build/deploy toolchain).
- **camera.ui SDK (Go)** for the plugin runtime, the `Notifier` RPC surface, and device storage.
- Delivery via each backend's plain HTTP API using the Go stdlib `net/http` — no third-party HTTP
  client libraries.
- Device persistence via the plugin's own `DeviceStorage` (a schema-registered `devices` key
  holding the device list as JSON) — no SQLite; the device count is tiny.

## Prerequisites

- **Go 1.26+** (to build the plugin binary).
- **Node.js 22+** and the plugin's dev dependencies (`npm install`) to produce the `contract.cjs`
  bundle via the camera.ui CLI.
- A running camera.ui instance you control (the host where the plugin gets installed).

## Build & deploy locally

camera.ui loads the **built artifact**, not source — a `git pull` alone does nothing. You must
build the bundle (`contract.cjs` + the platform binary) and copy it into the install slot.

The install slot is:

```
<camera.ui-install>/plugins/@calebcall/camera-ui-notify/
```

and the plugin's device data lives under
`<camera.ui-install>/volume/plugins/storage/@calebcall/camera-ui-notify/` — reinstalling the code
does not touch it.

> **Unlike the NVR plugin**, there is no hardcoded-package-id requirement here: the camera.ui host
> discovers notifiers by the `Notifier` **interface** declared in the plugin's contract, not by a
> fixed package name. This plugin can be installed under its own real name/namespace.

### Option A — build natively on the server (preferred)

If Go 1.26+ and Node are available on the host, build there and skip the cross-compile round-trip:

```bash
# 1. Get the latest source
cd ~/path/to/plugins && git pull
cd camera-ui-notify
npm install                 # first time only — pulls the camera.ui CLI bundler

# 2. Build the bundle (produces bundle/{contract.cjs, package.json, dist/bin/plugin})
npm run bundle:dev

# 3. Install into the @calebcall/camera-ui-notify slot
D=<camera.ui-install>/plugins/@calebcall/camera-ui-notify
mkdir -p "$D" && rm -rf "$D"/* && cp -a bundle/. "$D/"

# 4. First time only: enable it — remove the "@calebcall/camera-ui-notify" line
#    from disabledPlugins in <camera.ui-install>/volume/camera.ui.yaml

# 5. Restart camera.ui
systemctl restart cameraui   # or however your instance is managed
```

### Option B — cross-compile from a workstation

Build a statically-linked Linux binary locally, then ship the bundle to the server:

```bash
cd camera-ui-notify

# 1. Produce contract.cjs (+ package.json) under bundle/
npm install
npm run bundle:dev

# 2. Cross-compile the Linux binary into the bundle's dev path
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -ldflags "-s -w" -o bundle/dist/bin/plugin ./src/
chmod 755 bundle/dist/bin/plugin

# 3. Ship it (strip macOS junk so the server dir stays clean)
COPYFILE_DISABLE=1 tar czf /tmp/notify-install.tgz -C bundle .
scp /tmp/notify-install.tgz root@YOUR_SERVER:/tmp/

# 4. On the server: install into the slot, enable (first time), restart
ssh root@YOUR_SERVER '
  D=<camera.ui-install>/plugins/@calebcall/camera-ui-notify
  mkdir -p "$D" && rm -rf "$D"/* && tar xzf /tmp/notify-install.tgz -C "$D"
  systemctl restart cameraui
'
```

Set `GOARCH=arm64` for ARM hosts. The binary is resolved from the dev path
`<slot>/dist/bin/plugin` before any platform `node_modules` package, so no npm publish is involved.

### Verify it loaded and is being used

Tail the camera.ui log after restart:

```bash
grep -iE "Notify|Spawning Go|notify: rpc" <camera.ui-install>/volume/camera.ui.log | tail
```

You want to see the plugin spawn (`Spawning Go plugin ... dist/bin/plugin`) **and** the host
actually calling into it — e.g. `notify: rpc getDevices` / `notificationSettings` /
`sendNotification`.

## Adding a new backend

1. Create `src/backend/<name>.go` implementing the `Backend` interface (`ID`, `Label`, `Schema`,
   `ParseTarget`, `Send`) — see `ntfy.go`, `gotify.go`, or `webhook.go` for the shape.
2. Gate every schema field with `Condition: []sdk.SchemaCondition{{Key: "service", Value: ID()}}`
   so it only renders when the new backend is selected.
3. Call `backend.Register(new<Name>())` from an `init()` function in the same file.
4. Bump the version in `package.json`.

Nothing outside `src/backend/` needs to change — the registry, the `service` enum, and the
dispatch logic in `notifier.go` all pick up the new backend automatically.

## Development

```bash
go test ./src/...                 # full suite
go test ./src/... -race -count=1  # race detector
```

## License

[MIT](./LICENSE.md).
