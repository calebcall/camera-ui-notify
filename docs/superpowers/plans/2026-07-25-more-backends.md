# Add backends: Pushover, Telegram, Discord

Add three new notifier backends to camera-ui-notify. Each is a new
`src/backend/<name>.go` + `<name>_test.go`, implementing the existing `Backend`
interface and self-registering from `init()`. **No other files change** — the
service enum + settings form are built dynamically from `backend.All()`, and no
new Go dependencies are needed (stdlib `net/http`, `mime/multipart`,
`encoding/json` only).

## Shared conventions (match the existing ntfy/gotify/webhook backends)

- Read `src/backend/{ntfy.go,gotify.go,webhook.go}` first for the exact style:
  the `Backend` interface (`ID/Label/Schema/ParseTarget/Send`), `PriorityScale`,
  an injectable `*http.Client` field (default 10s timeout, overridden in tests),
  non-2xx → error with status + truncated (512B) body, and self-registration via
  `init(){ Register(new<Name>()) }`.
- **Schema fields**: namespaced by service id, each gated on the service
  selector via `Condition: []sdk.SchemaCondition{{Key:"service", Value:"<id>"}}`,
  and `Store: &true`. Secrets use `Format: sdk.StringFormatPassword`.
- **ParseTarget**: validate required fields (empty → error); return
  `map[string]string` with SHORT keys (`token`, `user`, `chat`, `webhook`, …)
  that `Send` reads — the namespaced form keys map to short cfg keys here, so
  `Send` never sees the `<id>_` prefix. (Same pattern ntfy/gotify use.)
- **Image**: when `len(n.Thumbnail) > 0`, deliver it as an image (see each
  backend below). All three support images.
- **Deep link**: `n.DeepLink` is already absolutized by the plugin when the
  user set a base URL, so it may be absolute (`https://…/cameras/…`) or a bare
  relative path. Only emit a clickable link/button when it starts with `http`
  (i.e. `strings.HasPrefix(n.DeepLink, "http")`); skip it otherwise (a relative
  path is not a valid external URL for these services).
- **Body text**: use `n.Body` and fall back to `n.Title` when body is empty
  (as gotify does), for services with a single text field.
- **Tests**: table/httptest-driven, NO real network. Assert endpoint/path,
  payload fields, image presence when Thumbnail set, priority/color mapping,
  link presence only when DeepLink is absolute, and the non-2xx error path.
  Each backend with a fixed hosted endpoint (Pushover, Telegram) must expose an
  injectable base URL so tests point at `httptest` instead of the real service.

---

## 1. Pushover (`src/backend/pushover.go`, id `pushover`, label `Pushover`)

- **Schema** (gated `service==pushover`): `pushover_token` (Title "API Token/Key",
  required, password), `pushover_user` (Title "User or Group Key", required,
  password).
- **ParseTarget**: token + user required → cfg `{token, user}`.
- **Send**: POST to the messages endpoint (default
  `https://api.pushover.net/1/messages.json`, injectable base URL for tests).
  Fields: `token`, `user`, `title` = n.Title, `message` = n.Body||n.Title (must
  be non-empty — Pushover rejects empty message), `priority` (see below), and
  when DeepLink is absolute: `url` = n.DeepLink, `url_title` = "Open camera".
  - Priority mapping: Info→0 (normal), Warn/Error/Critical→1 (high). Do NOT use
    Pushover priority 2 (emergency) — it requires `retry`/`expire` and repeats
    until acknowledged, wrong for detection notifications.
  - Image: when `n.Thumbnail` present, send as `multipart/form-data` with the
    text fields above plus a file part named `attachment`
    (filename `snapshot.jpg`, `Content-Type: image/jpeg`). Otherwise send
    `application/x-www-form-urlencoded`.

## 2. Telegram (`src/backend/telegram.go`, id `telegram`, label `Telegram`)

- **Schema** (gated `service==telegram`): `telegram_token` (Title "Bot Token",
  required, password), `telegram_chat` (Title "Chat ID", required).
- **ParseTarget**: token + chat required → cfg `{token, chat}`.
- **Send**: Bot API base default `https://api.telegram.org` (injectable for
  tests); method path is `/bot{token}/{method}`.
  - Text (no thumbnail): `sendMessage` — JSON `{chat_id, text}` where text =
    `n.Title` + "\n" + n.Body (omit the newline+body when body empty). When
    DeepLink is absolute, add `reply_markup` =
    `{"inline_keyboard":[[{"text":"Open camera","url": n.DeepLink}]]}`.
  - Image (thumbnail present): `sendPhoto` — `multipart/form-data` with
    `chat_id`, a `photo` file part (filename `snapshot.jpg`), `caption` = the
    same title/body text, and the same `reply_markup` (as a form field, JSON
    string) when DeepLink is absolute.
  - No severity/priority concept in Telegram — do not map one.

## 3. Discord (`src/backend/discord.go`, id `discord`, label `Discord`)

- **Schema** (gated `service==discord`): `discord_webhook` (Title "Webhook URL",
  required, password).
- **ParseTarget**: webhook required → cfg `{webhook}`.
- **Send**: POST the webhook URL (user-provided; tests point it at httptest — no
  base override needed).
  - Build one embed: `{title: n.Title, description: n.Body, color: <bySeverity>}`;
    add `url: n.DeepLink` only when DeepLink is absolute (makes the title a link).
  - Color by severity: Info `0x3498db` (blue), Warn `0xf1c40f` (yellow),
    Error/Critical `0xe74c3c` (red).
  - Text (no thumbnail): JSON `{embeds:[embed]}`.
  - Image (thumbnail present): `multipart/form-data` with `payload_json` =
    `{embeds:[{...embed, image:{url:"attachment://snapshot.jpg"}}]}` and a file
    part `files[0]` (filename `snapshot.jpg`). Discord returns 204 on success —
    treat 200–299 as OK.

---

## Verify
`cd <repo> && go build ./src/... && go vet ./src/... && go test ./src/... -race -count=2` all clean. Then a quick manual check that `backend.All()` now returns 6 backends (ntfy, gotify, webhook, pushover, telegram, discord) and `NotificationSettings()`/`StorageSchema()` include all their fields with unique keys.

## Docs / release (after backends land + review)
Update README (list the three new services + their config fields + which support images — all three do) and CHANGELOG (`## [0.4.0]`), bump package.json to 0.4.0.
