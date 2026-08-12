<h1 align="center">Notify</h1>

<p align="center">
  A fully-local, multi-backend <a href="https://github.com/seydx/camera.ui">camera.ui</a> notifier
  plugin — delivers notifications to a service you control (<a href="https://ntfy.sh">ntfy</a>,
  <a href="https://gotify.net">Gotify</a>, Pushover, Telegram, Discord,
  <a href="https://grafana.com">Grafana</a>, or a generic webhook).
</p>

---

## Why this exists


There is intentionally no notifier in the open ecosystem: the closed, official NVR bundles a notifier that pushed to the camera.ui mobile app through camera.ui's proprietary FCM/APNs cloud relay. This plugin takes the only fully-local road to real background push instead: delivering to a push/webhook service *you* run or control.

This plugin is a pure **notifier**. It is decoupled from any single publisher — it delivers notifications from **any** publisher, including our NVR plugin's detection events and camera.ui's own system notifications, not just one plugin's output.

## How it works

One plugin, contract `interfaces: [Notifier]`, built around a pluggable-backend (strategy) pattern:

- A `Backend` interface abstracts one delivery service (id, label, config schema, target validation, and delivery).
- A package-level registry maps each backend's stable id to its implementation; every backend self-registers from its own `init()`.
- The plugin holds **one active target** in its own persisted config (`StorageSchema`): which service is selected, plus that service's validated fields. `getDevices` synthesizes a single `sdk.NotifierDevice` from this config on every call — there is no device registry.
- `sendNotification` dispatches the synthesized device to its backend's `Send`. A backend failure is logged and returned to the host.

### Why config, not device registration

Earlier versions of this plugin modeled targets as registrable `NotifierDevice`s, the way camera.ui's own mobile-push notifier does. That model turned out to be unreachable from the stock UI: `registerDevice` is only ever called by the camera.ui mobile app's push-registration flow, which is hardcoded to the official NVR's plugin name. The generic notification settings panel renders `notificationSettings()` read-only — it has no "add a device" affordance for third-party plugins. The one part of the stock UI that **does** render an editable, savable form for a third-party plugin is its own settings page, which renders whatever `StorageSchema` the plugin declares. So Notify now models its target as plugin config instead of a device: you configure it once, in the plugin's own settings, and `getDevices` synthesizes the device the host's `Notifier` interface expects from that config.

Adding a new backend later is **one new file** — `src/backend/<name>.go` implementing `Backend` plus `Register(...)` in its `init()` — and a version bump. No new plugin, no core change, no changes to `plugin.go`.

## Backends (v1)

Severity is mapped consistently across backends via `backend.PriorityScale`, which spreads camera.ui's four severity levels (`info` → `warn` → `error` → `critical`) evenly across each backend's native priority range, `info` at the low end and `critical` at the high end. Grafana is the exception: no Grafana surface has a numeric priority, so severity travels verbatim as a label/tag.

### ntfy

Publishes to [ntfy.sh](https://ntfy.sh) or a self-hosted ntfy server.

| Field    | Required | Default              | Notes                                              |
| -------- | -------- | --------------------- | --------------------------------------------------- |
| `server` | no       | `https://ntfy.sh`     | Base URL of the ntfy server. Trailing `/` trimmed. |
| `topic`  | yes      | —                     | The ntfy topic to publish to.                      |
| `token`  | no       | —                     | Access token for a protected/self-hosted topic, sent as `Authorization: Bearer <token>`. |

Delivery: `POST {server}/{topic}` with the notification body as the request body, plus `Title`, `Priority` (1–5, from severity), `Click` (deep link, if set), `Attach`/`Icon` (image URL, if set), and `Actions` (a "Play clip" view button, if a video clip is set) headers.

### Gotify

Publishes to a self-hosted [Gotify](https://gotify.net) server.

| Field    | Required | Notes                                          |
| -------- | -------- | ----------------------------------------------- |
| `server` | yes      | Base URL of the Gotify server. Trailing `/` trimmed. |
| `token`  | yes      | Gotify application token, used to authenticate published messages. |

Delivery: `POST {server}/message?token={token}` with a JSON body `{title, message, priority}` (priority 0–10, from severity), plus a `client::notification.click` extra for the deep link and a `bigImageUrl` extra when an image URL is set. A video clip is appended to the message text as a `Play clip: <url>` line.

### Generic webhook

Delivers to any HTTP endpoint you provide — the fallback for anything without a dedicated backend.

| Field         | Required | Default | Notes                                                         |
| ------------- | -------- | ------- | -------------------------------------------------------------- |
| `url`         | yes      | —       | Endpoint that receives the notification.                       |
| `method`      | no       | `POST`  | `POST` or `PUT`.                                                |
| `headerName`  | no       | —       | Optional custom header name (e.g. for a shared secret). Requires `headerValue` if set. |
| `headerValue` | no       | —       | Value of the custom header. Requires `headerName` if set.       |

Delivery: `{method} {url}` with `Content-Type: application/json` and (if configured) the custom header, carrying a JSON body of `{title, subtitle, body, severity, tag, silent, imageUrl, videoUrl, deepLink, data, createdAt, thumbnailBase64}`.

### Pushover

Hosted push to the [Pushover](https://pushover.net) app.

| Field    | Required | Notes                                          |
| -------- | -------- | ---------------------------------------------- |
| `token`  | yes      | Pushover application API token/key.            |
| `user`   | yes      | Your Pushover user or group key.               |

Delivery: `POST https://api.pushover.net/1/messages.json` with title/message and a priority (Info→0 normal, everything higher→1 high; never emergency). The snapshot **image** is sent as an `attachment`, and an absolute deep link becomes a supplementary `url` titled "Open camera" for a detection or "Open in camera.ui" for anything else. A video clip takes that `url` slot when no deep link claims it, and otherwise appends a `Play clip: <url>` line to the message.

### Telegram

Delivers to a chat via a [Telegram bot](https://core.telegram.org/bots).

| Field   | Required | Notes                                                    |
| ------- | -------- | -------------------------------------------------------- |
| `token` | yes      | Bot token from @BotFather.                               |
| `chat`  | yes      | Chat ID to deliver to.                                   |
| `clip`  | no       | **Upload video clips** — off by default. See [Video clips](#video-clips-video-in-push). |

Delivery: `sendPhoto` (with the snapshot **image** + caption) when a thumbnail is present, otherwise `sendMessage` — or `sendVideo` when clip upload is on and the notification carries a clip. An absolute deep link is added as an inline button, labelled "Open camera" when it opens a camera page and "Open in camera.ui" otherwise; a video clip adds a second "Play clip" button below it, unless it is being uploaded.

### Discord

Delivers to a channel via a Discord [webhook](https://support.discord.com/hc/en-us/articles/228383668).

| Field     | Required | Notes                                   |
| --------- | -------- | --------------------------------------- |
| `webhook` | yes      | Channel webhook URL.                    |
| `clip`    | no       | **Upload video clips** — off by default. See [Video clips](#video-clips-video-in-push). |

Delivery: a rich embed (title, body, severity color — blue/yellow/red) with the snapshot **image** attached; an absolute deep link makes the title a link, and a video clip is appended to the description as a `Play clip` link — or uploaded as a second attachment when clip upload is on.

### Grafana

Delivers to the [Grafana](https://grafana.com) ecosystem. This is not one ingest endpoint, so a
**Mode** field selects which surface receives the notification — and the three modes address three
different services, so each has its own connection fields.

| Field                   | Required | Mode          | Notes                                                    |
| ----------------------- | -------- | ------------- | --------------------------------------------------------- |
| `grafana_mode`          | yes      | —             | `annotations`, `alertmanager`, or `irm`. Defaults to `annotations`. |
| `grafana_server`        | yes      | annotations   | Base URL of the Grafana instance. Trailing `/` trimmed.   |
| `grafana_token`         | yes      | annotations   | Service-account token, sent as `Authorization: Bearer <token>`. |
| `grafana_tags`          | no       | annotations   | Comma-separated extra tags.                               |
| `grafana_am_url`        | yes      | alertmanager  | Base URL of the **Alertmanager**, not of Grafana — **including any path prefix** (see below). A pasted `.../api/v2/alerts` endpoint is accepted and trimmed. |
| `grafana_am_user`       | no       | alertmanager  | Basic-auth username. For Grafana Cloud, the numeric Alertmanager instance ID. |
| `grafana_am_password`   | no       | alertmanager  | Basic-auth password. For Grafana Cloud, an Access Policy token with the `alerts:write` scope. Required if a username is set, and vice versa. |
| `grafana_alertname`     | no       | alertmanager  | `alertname` label. Defaults to `CameraUINotification`.    |
| `grafana_ttl`           | no       | alertmanager  | Seconds before Alertmanager auto-resolves the alert. Default `900`, minimum `30`. |
| `grafana_irm_url`       | yes      | irm           | Inbound webhook URL of an IRM / OnCall integration. The token is in the URL, so it is masked and kept out of every error message. |

**Annotations** — `POST {server}/api/annotations` with a point-in-time, organization-wide
annotation tagged `camera.ui`, `camera:<name>`, `severity:<level>`, plus your extra tags. Surface it
on a dashboard with an annotation query filtered on the `camera.ui` tag; that survives dashboard
renames, which pinning to a dashboard UID would not. The tooltip text carries the title, the body,
and — when `base_url` is set — a link back to camera.ui.

**Alertmanager** — `POST {alertmanager}/api/v2/alerts`, straight to an Alertmanager's own API, with
optional basic auth. Works with a standalone Prometheus Alertmanager, Mimir/Cortex, or Grafana
Cloud's hosted Alertmanager (username = instance ID, password = API token).

> **This mode does not talk to Grafana**, and it deliberately cannot. Grafana's built-in
> Alertmanager will not accept posted alerts: its route table exposes `GET` for alerts but declares
> the `POST` route only as `/alertmanager/{DatasourceUID}/api/v2/alerts`, a proxy to an *external*
> Alertmanager, with no `grafana` variant. Grafana-managed alerts can only come from Grafana's own
> rule evaluation. Point this at a real Alertmanager. (Versions 0.6.0–0.6.1 targeted Grafana here
> and always failed with `400 bad request data`.)

> **Get the URL right — this is the most common way to misconfigure this mode.** Mimir and Grafana
> Cloud serve the Alertmanager API under a path prefix, `/alertmanager` by default; a standalone
> Alertmanager serves it at the root. Omit the prefix and every send fails with a bare
> `404 page not found`.
>
> | Target | Enter | Resulting POST |
> | --- | --- | --- |
> | Grafana Cloud / Mimir | `https://alertmanager-prod-xx.grafana.net/alertmanager` | `…/alertmanager/api/v2/alerts` |
> | Standalone Alertmanager | `http://alertmanager:9093` | `…/api/v2/alerts` |
>
> **Grafana Cloud credentials** come from two different places. The **username** is the numeric
> Alertmanager instance ID, shown with the URL on the Alertmanager details page in the Cloud
> portal. The **password** is an **Access Policy token** (`glc_…`) carrying the `alerts:write`
> scope, created under Access Policies — *not* a Grafana service-account token (`glsa_…`), which
> authenticates to Grafana rather than to the Alertmanager.

`endsAt` is `now + grafana_ttl`, which lets Alertmanager auto-resolve the alert without a
second request. `startsAt` is deliberately not sent — Alertmanager stamps it from its own clock.

> **If sends succeed but no alert appears, check the clock.** `endsAt` has to be absolute
> (Alertmanager's API has no relative form), so it is computed from the camera.ui host's clock. A
> host running more than `grafana_ttl` *behind* the Alertmanager sends an `endsAt` already in the
> past: the alert is accepted with a `200`, resolved on arrival, and never shows as active. The
> symptom is a clean `notify: delivered` in the log and an empty
> `GET {alertmanager}/api/v2/alerts`. Keep the host in NTP sync. The 900-second default exists to
> give that failure some margin.
>
> Second, gentler trap: alerts self-resolve after `grafana_ttl` and drop off the active list, so
> when testing, look within the window rather than an hour later. Labels are `alertname`, `source=camera.ui`, `severity` (camera.ui's own four
levels, verbatim), `camera`, `camera_id`, and a unique `event_id` — the last of these matters, because
Alertmanager deduplicates on the label set and without it two detections on one camera inside the
TTL window would collapse into a single alert. The absolute deep link becomes `generatorURL`,
which Alertmanager shows as **Source**.

**IRM** — `POST {integration URL}`. IRM renders each alert group through templates chosen by the
integration's *type*, and the two types people actually create read different bodies, so the
payload carries both: Grafana Alerting's webhook envelope (`status`, `alerts[]` with
labels/annotations/`generatorURL`/`imageURL`, `groupKey`, `commonLabels`, `externalURL`) for a
**Grafana Alerting** integration, and OnCall's formatted-webhook fields (`alert_uid`, `image_url`,
`link_to_upstream_details`) for a **Webhook** integration. `title`, `message`, and
`state=alerting` are read by both. One body, correct under either type, nothing to configure.

Alert groups are keyed **per camera** — `camera.ui:<camera>`, falling back to `camera.ui` for a
notification that names no camera — so one busy camera can't bury a quiet one. Within a group each
event keeps its own `fingerprint`, so detections stay individually visible.

**IRM groups do not auto-resolve.** IRM decides that from a template on the payload's status — its
default is `{{ payload.status == "resolved" }}` — so a group closes only when a *second* request
arrives saying so. `endsAt` is ignored, which is why alerts carry the documented never-resolves
sentinel rather than a future time that would imply a close that never comes. This plugin sends one
stateless POST per event and no follow-up, by design: a delayed resolve would mean a background
timer and per-event state, and a restart would strand the group open anyway. Close them in IRM.

> **Camera names:** `camera` carries the camera's display name, taken from `Data["cameraName"]`
> when a publisher supplies one and otherwise from the deep link, which camera.ui routes by name.
> `Data["cameraId"]` is a UUID for the publishers seen so far, so it is kept separately as
> `camera_id` (alertmanager and IRM modes) for routing rules that must survive a rename. With
> neither a name nor a deep link, `camera` falls back to the id.

> **Images:** ntfy, Pushover, Telegram, and Discord all render the detection snapshot. Gotify is
> text + link only (it needs a hosted image URL, which this fully-local plugin doesn't provide).
> Grafana renders one only in IRM mode, and only when the publisher supplied a hosted `ImageURL` —
> annotations have no image field at all, and alerts carry the URL as an `image_url` annotation
> that Grafana itself won't render but downstream notification templates can use.

> **Video clips:** annotations mode adds a `Play clip` anchor to the annotation tooltip; the
> alertmanager and IRM modes carry the URL as a `video_url` annotation, alongside `image_url` and
> on the same terms — Grafana won't render it, downstream templates can use it.

> **Secrets in logs:** transport failures never log the bot token / webhook URL / other
> URL-embedded secret — request URLs are redacted from delivery errors.

## Follow-up updates (AI descriptions)

camera.ui announces a detection immediately, then republishes the same notification a few seconds later once the AI description is ready. Both publishes carry the same `tag` (the collapse key), and the second carries `silent: true`, meaning *this only updates the first one — don't alert again*.

Handled per backend, according to what each platform can actually do:

| Backend         | Follow-up behaviour                                                                  |
| --------------- | ------------------------------------------------------------------------------------ |
| Telegram        | **Replaces** the original message (`editMessageText` / `editMessageCaption`).         |
| Discord         | **Replaces** the original message (`PATCH .../messages/{id}`).                        |
| Grafana         | **Revises** the record it already filed — see below.                                  |
| ntfy            | Delivered at priority `1` — visible, no sound or vibration.                           |
| Gotify          | Delivered at priority `3` — joins the in-app list, raises no system notification.      |
| Pushover        | Delivered at priority `-1` (quiet) — no sound or vibration.                            |
| Generic webhook | `silent: true` is forwarded in the JSON payload; your endpoint decides.               |

Grafana revises per mode: **annotations** patches the annotation it created (`PATCH /api/annotations/:id`), so the dashboard keeps one marker at the detection's own timestamp whose text improves. **Alertmanager** and **IRM** re-file under the `event_id` / `alert_uid` the first alert used — that identity is what each surface deduplicates on, so the existing alert or group picks up the description instead of a second one firing. When the publisher supplies its own `Data["eventId"]`, that id is authoritative and the two publishes already share it, so those modes update correctly even across a plugin restart.

The replacing backends therefore show **one** notification whose text improves in place. The message id is remembered in memory per tag for 15 minutes; after a plugin restart, or if the original message was deleted, the update is delivered as a new (quiet) message instead of being lost.

Only the `silent` follow-up replaces. Detection tags repeat across events (`motion:cam-1` is the same tag every time that camera sees something), so a *new* alert reusing a tag always posts a new message — your chat history is never rewritten by a later event.

**Critical alerts ignore `silent`** — a `critical` severity notification always alerts, per the SDK contract.

If you would rather never see the follow-up on a backend that can't replace, set **Follow-up updates** to `Skip the update entirely` in the plugin settings. Backends that replace in place still receive it under that setting, since editing adds nothing to the notification list — with the one caveat that a lost message id (restart, deleted message) turns that edit into a new quiet message.

## Video clips ("Video in Push")

camera.ui 2.1.6 added `videoUrl` to the notification payload: a short MP4 of the recording that
triggered the alert, published when the camera — or, for a multi-camera episode, the episode —
has **Video in Push** switched on under its notification settings. Whether a clip is published at
all is decided there, per camera, not here — this plugin only forwards what it is handed.

### By default: the clip is a link

Nothing this plugin talks to is the first-party mobile app, so no backend plays the clip *inside*
the push the way an iOS attachment does. What each one can do is offer the clip as a link, opened
in the phone's own browser or player — already authenticated to your server. The rule is the same
everywhere: **the clip is added, never substituted.** The snapshot keeps its attachment slot and
the deep link keeps its click target, because the deep link opens the event in camera.ui, from
which the recording is one tap away, whereas the clip on its own is a dead end.

| Backend         | How the clip is surfaced                                                          |
| --------------- | ----------------------------------------------------------------------------------- |
| ntfy            | A `Play clip` **view action button** (the single `Attach` slot stays with the snapshot). |
| Telegram        | A second **inline button** below the deep-link button.                              |
| Discord         | A `Play clip` link at the end of the embed description.                             |
| Pushover        | The supplementary `url` when no deep link claims it; otherwise a line in the message. |
| Gotify          | A `Play clip: <url>` line appended to the message text.                             |
| Grafana         | An anchor in the annotation tooltip; a `video_url` annotation in alertmanager/IRM modes. |
| Generic webhook | A `videoUrl` field in the JSON payload, forwarded verbatim.                         |

**A linked clip has to be reachable from the phone.** camera.ui publishes the URL absolute; a
server-relative one is made absolute with the **camera.ui Base URL** setting, the same way deep
links are. Without that setting a relative clip URL is dropped rather than delivered as a link
that cannot open — except on the generic webhook, whose receiver is a machine that may well be
able to resolve it, so there it is forwarded as-is.

### Opt in: upload the clip (Telegram, Discord)

Telegram and Discord can carry the video itself, and both render a real player in the chat. Turn
on **Upload video clips** in the plugin settings for either one and the plugin downloads the clip
from camera.ui and re-uploads the bytes to the service. Nothing outside your network ever fetches
from your server — the plugin is the only client that touches the clip URL — so this works for an
install that isn't reachable from the internet at all.

| | Telegram | Discord |
| --- | --- | --- |
| Method | `sendVideo`, `supports_streaming` on so it plays while downloading | a `clip.mp4` file attachment beside the embed |
| Snapshot | **replaced** — Telegram carries one media item per message | **kept**, still rendered inside the embed |
| Size cap | 50 MB (the Bot API's own upload limit) | 8 MB (Discord allows 10 MB per request on an unboosted server; the headroom is for the snapshot) |
| Follow-up cost | none — `editMessageCaption` leaves the video in place, so the AI description doesn't re-download it | one more download + upload — Discord's edit drops any attachment the request doesn't re-send |

It is off by default because it is the expensive path: the default merely passes a URL along,
while this moves the whole file twice for every detection. The Discord row above is the one to
weigh — a busy camera with clip upload on moves each clip **four** times once the AI description
lands.

**Every failure falls back to the link, never to a lost notification.** A clip past the size cap
is not downloaded at all (a `Content-Length` over the limit fails before the body is read); a
download that errors or stalls, and an upload the service rejects, both retry immediately as an
ordinary send with the `Play clip` link. Oversize clips are rejected rather than truncated — a
clipped MP4 is a broken upload, not a smaller video. Each fallback is logged with its reason.

## Configuring your target (v1: one active target)

There is no "add device" flow. Instead, configure the plugin itself:

1. Open the **Notify** plugin's page in camera.ui (Plugins → Notify).
2. In its settings, pick a **Service** (`ntfy`, `Gotify`, `Generic webhook`, `Pushover`, `Telegram`, `Discord`, or `Grafana`) from the dropdown built from the registered backends.
3. Fill in that service's fields — only the selected service's fields are shown; the rest are condition-gated out.
4. Optionally set **Follow-up updates** (see [Follow-up updates](#follow-up-updates-ai-descriptions)) — defaults to delivering the AI description quietly.
5. On Telegram or Discord, optionally turn on **Upload video clips** (see [Video clips](#video-clips-video-in-push)) — off by default.
6. Save. The config is validated (`ParseTarget`) the next time a notification is dispatched; `getDevices` then synthesizes one delivery target from it, and notifications from any publisher are delivered there.

This is a **single, instance-wide target** in v1 — there's no way to register several devices at once. Changing the config replaces the previous target rather than adding to it. Delivery for that one target is a single request per notification (no fan-out to worry about, since there's only one device).

## Tech stack

- **Go** (matches the NVR plugin; same build/deploy toolchain).
- **camera.ui SDK (Go)** for the plugin runtime, the `Notifier` RPC surface, and device storage.
- Delivery via each backend's plain HTTP API using the Go stdlib `net/http` — no third-party HTTP client libraries.
- Config persistence via the plugin's own `DeviceStorage`, holding the selected service and its fields as declared by `StorageSchema()` — no SQLite; it's a handful of scalar values.

## Prerequisites

- **Go 1.26+** (to build the plugin binary).
- **Node.js 22+** and the plugin's dev dependencies (`npm install`) to produce the `contract.cjs` bundle via the camera.ui CLI.
- A running camera.ui instance you control (the host where the plugin gets installed).

## Build & deploy locally

camera.ui loads the **built artifact**, not source — a `git pull` alone does nothing. You must build the bundle (`contract.cjs` + the platform binary) and copy it into the install slot.

The install slot is:

```
<camera.ui-install>/plugins/@calebcall/camera-ui-notify/
```

and the plugin's config (the selected service and its fields) lives under `<camera.ui-install>/volume/plugins/storage/@calebcall/camera-ui-notify/` — reinstalling the code does not touch it.

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

# 4. Restart camera.ui
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
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \ go build -ldflags "-s -w" -o bundle/dist/bin/plugin ./src/
chmod 755 bundle/dist/bin/plugin

# 3. Ship it (strip macOS junk so the server dir stays clean)
COPYFILE_DISABLE=1 tar czf /tmp/notify-install.tgz -C bundle .
scp /tmp/notify-install.tgz root@YOUR_SERVER:/tmp/

# 4. On the server: install into the slot, enable (first time), restart
ssh root@YOUR_SERVER 'D=<camera.ui-install>/plugins/@calebcall/camera-ui-notify mkdir -p "$D" && rm -rf "$D"/* && tar xzf /tmp/notify-install.tgz -C "$D"; systemctl restart cameraui'
```

Set `GOARCH=arm64` for ARM hosts. The binary is resolved from the dev path `<slot>/dist/bin/plugin` before any platform `node_modules` package, so no npm publish is involved.

### Verify it loaded and is being used

Tail the camera.ui log after restart:

```bash
grep -iE "Notify|Spawning Go|notify: rpc" <camera.ui-install>/volume/camera.ui.log | tail
```

You want to see the plugin spawn (`Spawning Go plugin ... dist/bin/plugin`) **and** the host actually calling into it — e.g. `notify: rpc getDevices` / `notificationSettings` / `sendNotification`.

## Adding a new backend

1. Create `src/backend/<name>.go` implementing the `Backend` interface (`ID`, `Label`, `Schema`, `ParseTarget`, `Send`) — see `ntfy.go`, `gotify.go`, or `webhook.go` for the shape.
2. Gate every schema field with `Condition: []sdk.SchemaCondition{{Key: "service", Value: ID()}}` so it only renders when the new backend is selected.
3. Call `backend.Register(new<Name>())` from an `init()` function in the same file.
4. Bump the version in `package.json`.

Nothing outside `src/backend/` needs to change — the registry, the `service` enum, and the
dispatch logic in `notifier.go` all pick up the new backend automatically.

## Development

```bash
go test ./src/...                 # full suite
go test ./src/... -race -count=1  # race detector (needs cgo: apt-get install gcc)
npm run lint                      # golangci-lint, configured by .golangci.yml
npm run format                    # gofmt + go fix
```

Linting is golangci-lint only. It bundles staticcheck's analyzers (`SA`/`S`/`ST`/`QF`), so running
the standalone `staticcheck` binary alongside it only duplicates findings — and, being a separate
tool, it can't read the path-scoped exclusions in `.golangci.yml`.

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

`.golangci.yml` excludes three deliberate idioms, each scoped as narrowly as the linter allows so
the check stays live everywhere else: `defer resp.Body.Close()` (errcheck), and — in `_test.go`
only — passing a nil `Context` to exercise each backend's `ctx == nil` fallback (SA1012) and the
`!(a <= b && b <= c)` monotonicity assertions (QF1001).

## License

[MIT](./LICENSE.md).
