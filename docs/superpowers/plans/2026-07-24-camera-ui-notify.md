# camera.ui Notify — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A single, fully-local camera.ui notifier plugin that delivers notifications to a user-selected backend (ntfy, Gotify, or a generic webhook in v1), where adding a new backend later is one new file + a version bump.

**Architecture:** One `Notifier`-interface plugin. A `Backend` interface abstracts one delivery service; a package-level registry maps backend id → implementation (each backend self-registers from `init()`). The Notifier *device* model carries per-device backend selection (each device = one target bound to one backend, holding that backend's config). `SendNotification` dispatches each device to its backend's `Send`. `NotificationSettings` builds a conditional registration form from the registry.

**Tech Stack:** Go 1.26 + `github.com/cameraui/sdk/go v1.1.11` (+ `rpc/go v1.0.6`, `google/uuid`). Delivery via Go stdlib `net/http`. Device persistence via the plugin's own `sdk.DeviceStorage`.

## Global Constraints

- Package name `@calebcall/camera-ui-notify`, `displayName: "Notify"`, `main: ./main.go`.
- Contract: `role: Hub`, `interfaces: [Notifier]` ONLY — no `NVR`, no `OAuthCapable`, no `PublishNotifications` capability, no other interface. (Declaring an unimplemented interface is the exact bug removed from the NVR — see `@calebcall/camera-ui-nvr-local` contract note.)
- Fully local: no cloud, no license, no outbound calls except to user-configured backend endpoints.
- Exported Go methods become RPC methods (wire name = first-letter-lowercased); every wire name MUST be listed in `RPCMethods()` or it is never registered.
- `sdk.DeviceStorage.SetValue` silently no-ops for a key with no declared schema — every persisted key MUST be declared in `StorageSchema()` with `Store: true`.
- Commit messages: no `Co-Authored-By`, no "Claude"/"Generated with Claude Code" lines.
- Reference implementations (read, don't re-derive): the NVR plugin at `../camera-ui-nvr-local/` (scaffold, `NewPlugin`, `RPCMethods`, `StorageSchema`, build/deploy) and the SDK's `externals/sdk/go/plugin_notifier.go` (`NotifierDevice`, `Notification`, `Severity*`) and `storage_schema.go` (`JsonSchema`, `SchemaCondition`, `StringFormat`).

**SDK reference types (authoritative — do not redefine):**
```go
type NotifierDevice struct {
    ID string; OwnerUserID string; Name string; Active bool; Metadata map[string]any
}
type Notification struct {
    Title, Subtitle, Body string; Severity Severity; Tag string
    Thumbnail []byte; ImageURL, DeepLink string; Data map[string]string
}
const ( SeverityInfo="info"; SeverityWarn="warn"; SeverityError="error"; SeverityCritical="critical" )
type SchemaCondition struct { Key string; Value any; Operator SchemaConditionOperator }
// JsonSchema has: Type, Key, Title, Description, Enum []string, DefaultValue, Placeholder,
//   Required bool, Hidden bool, Store *bool, Format StringFormat, Condition []SchemaCondition
```

**Notifier RPC surface the host calls (wire names → exported Go methods):**
```go
GetDevices(ownerUserIDs []string) ([]sdk.NotifierDevice, error)        // getDevices
GetDevice(deviceID string) (*sdk.NotifierDevice, error)                // getDevice (nil = not ours)
RegisterDevice(ownerUserID string, input map[string]any) (sdk.NotifierDevice, error) // registerDevice
RevokeDevice(deviceID string) error                                    // revokeDevice
UpdateDevice(deviceID string, patch map[string]any) (*sdk.NotifierDevice, error) // updateDevice (nil = not ours)
SendNotification(deviceIDs []string, n sdk.Notification) error         // sendNotification
NotificationSettings() ([]sdk.JsonSchema, error)                       // notificationSettings
```
> Task 1 must confirm these exact signatures against `plugin_notifier.go` / how the host proxy calls them; adjust arg/return types to match the SDK if they differ (e.g. `*Notification` vs `Notification`).

---

### Task 1: Scaffold + Notifier-only contract

**Files:**
- Create: `package.json`, `go.mod`, `cameraui.config.ts`, `contract.ts`, `src/index.ts`, `main.go`, `.gitignore`, `LICENSE.md`
- Create: `src/plugin.go` (skeleton), `src/contract_test.go`

**Interfaces produced:** `NotifyPlugin` struct + `NewPlugin(logger *sdk.Logger, api *sdk.PluginAPI, storage *sdk.DeviceStorage) sdk.Plugin`; empty `RPCMethods() []string`; `StorageSchema() []sdk.JsonSchema` (declares the `devices` key, Task 3 uses it).

Copy the scaffold shape from `../camera-ui-nvr-local/` and adapt: `go.mod` module `github.com/calebcall/camera-ui-notify`; `main.go` = `func main(){ sdk.Run(NewPlugin) }`; `cameraui.config.ts` identical (go targets); `.gitignore` ignores `dist`, `bundle`, `node_modules`.

`contract.ts`:
```ts
import { PluginInterface, PluginRole } from '@camera.ui/sdk';
import type { PluginContract } from '@camera.ui/sdk';
export const contract: PluginContract = {
  name: 'Notify', role: PluginRole.Hub, provides: [], consumes: [],
  interfaces: [PluginInterface.Notifier], capabilities: [],
};
export default contract;
```

- [ ] **Step 1: Write `src/contract_test.go`** — parse `../contract.ts` (relative to test file: `contract.ts` at repo root), assert `interfaces` array contains `Notifier` and does NOT contain `NVR`, `OAuthCapable`, or `PublishNotifications`. (Mirror the NVR's `contract_test.go` regex approach.)
- [ ] **Step 2: Run it — expect FAIL** (`contract.ts` missing). `cd <repo> && go test ./src/ -run Contract`.
- [ ] **Step 3: Create the scaffold files above** + `src/plugin.go` skeleton: `NotifyPlugin` embedding `sdk.BasePlugin` (see NVR), `NewPlugin` storing `logger/api/storage`, `RPCMethods()` returning `[]string{}`, `StorageSchema()` returning the `devices` string key `{Key:"devices", Type:String, Hidden:true, Store:&true}`.
- [ ] **Step 4: Run** `go build ./src/... && go test ./src/ -run Contract` — expect PASS + clean build.
- [ ] **Step 5: `npm install && npm run bundle:dev`** — confirm `bundle/contract.cjs` is produced.
- [ ] **Step 6: Commit** `feat: scaffold Notify plugin (Notifier-only contract)`.

---

### Task 2: Backend interface + registry + severity mapping

**Files:** Create `src/backend/backend.go`, `src/backend/backend_test.go`

**Interfaces produced (consumed by Tasks 4–7):**
```go
package backend
import ( "context"; sdk "github.com/cameraui/sdk/go" )

type Backend interface {
    ID() string      // stable id: "ntfy" | "gotify" | "webhook"
    Label() string   // human label for the service dropdown
    Schema() []sdk.JsonSchema  // device-config fields, each Condition-gated to service==ID()
    ParseTarget(input map[string]any) (map[string]string, error) // validate → config; error on missing/invalid
    Send(ctx context.Context, cfg map[string]string, n sdk.Notification) error
}

func Register(b Backend)          // panics on duplicate id; call from init()
func Get(id string) (Backend, bool)
func All() []Backend              // snapshot sorted by ID(), stable for enum/schema

// Priority helpers backends reuse so severity mapping is consistent:
//   PriorityScale(sev, lo, hi) maps Info→lo.. Critical→hi across an integer range.
func PriorityScale(sev sdk.Severity, lo, hi int) int
```

- [ ] **Step 1: Write `backend_test.go`:** (a) register two fake backends, assert `Get` returns them, `Get("nope")` false, `All()` sorted by id; (b) `Register` panics on duplicate id; (c) `PriorityScale(Info,1,5)==1`, `PriorityScale(Critical,1,5)==5`, `Warn`/`Error` in between and monotonic.
- [ ] **Step 2: Run — expect FAIL** (package missing).
- [ ] **Step 3: Implement `backend.go`** — `registry map[string]Backend` guarded by a `sync.Mutex`; `Register` panics on dup; `All()` returns a slice sorted by `ID()`. `PriorityScale`: map the 4 severities to evenly-spaced points in `[lo,hi]` (Info=lo, Critical=hi).
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** `feat: backend interface + registry + severity mapping`.

---

### Task 3: Device store over DeviceStorage

**Files:** Create `src/store.go`, `src/store_test.go`

**Interfaces produced (consumed by Task 4):**
```go
type deviceStore struct { /* storage deviceStorage; mu sync.Mutex */ }
type deviceStorage interface { // subset of *sdk.DeviceStorage, for test fakes
    GetValue(key string, def ...any) any
    SetValue(key string, value any) error
}
func newDeviceStore(s deviceStorage) *deviceStore
func (d *deviceStore) List(ownerUserIDs []string) []sdk.NotifierDevice // empty owners = all
func (d *deviceStore) Get(id string) (sdk.NotifierDevice, bool)
func (d *deviceStore) Put(dev sdk.NotifierDevice) error   // upsert, persists
func (d *deviceStore) Delete(id string) (bool, error)     // returns whether it existed
```
Persistence: JSON-marshal the full `[]sdk.NotifierDevice` slice to the `devices` storage key (declared in Task 1's `StorageSchema`). Load lazily on first access; keep an in-memory copy guarded by `mu`; write-through on Put/Delete. `List(nil)` or empty → all devices; otherwise filter by `OwnerUserID ∈ ownerUserIDs`.

- [ ] **Step 1: Write `store_test.go`** with a `fakeStorage` implementing `deviceStorage` over a `map[string]any` (mirror the NVR recorder's `fakeCameraStorage`). Tests: Put then Get; List(nil) returns all; List([]string{"u1"}) filters by owner; Delete returns true then false; a *new* store over the *same* fakeStorage sees persisted devices (round-trip through the `devices` key).
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement `store.go`.**
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** `feat: device store backed by DeviceStorage`.

---

### Task 4: Notifier RPC methods + dispatch (plugin.go)

**Files:** Modify `src/plugin.go`; Create `src/notifier.go`, `src/notifier_test.go`

**Consumes:** `backend` registry (Task 2), `deviceStore` (Task 3).
**Produces:** the 7 Notifier RPC methods (signatures in Global Constraints) + their `RPCMethods()` entries.

Behaviour:
- `RegisterDevice(owner, input)`: read `input["service"]` (string); `backend.Get(service)` → error `"unknown service %q"` if absent; `b.ParseTarget(input)` → cfg (propagate error); build `NotifierDevice{ID: uuid.NewString(), OwnerUserID: owner, Name: nameFromInput(input) or service, Active: true, Metadata: {"service": service, ...cfg}}`; `store.Put`; return it.
- `GetDevices(owners)` → `store.List(owners)`. `GetDevice(id)` → `store.Get` (nil if absent). `RevokeDevice(id)` → `store.Delete`. `UpdateDevice(id, patch)`: load; apply `name`/`active` from patch if present; `Put`; return updated (nil if id unknown).
- `SendNotification(ids, n)`: for each id → `store.Get`; skip inactive/unknown; resolve `metadata["service"]` → `backend.Get`; extract cfg (metadata minus `service`); `b.Send(ctx, cfg, n)`; collect errors with `errors.Join`; a single backend failure never aborts the loop. Log each failure via the plugin logger.
- `NotificationSettings()`: return `[]JsonSchema{ serviceEnumField } + flatten(b.Schema() for b in backend.All())`. The `service` field: `{Key:"service", Type:String, Title:"Service", Enum: [b.ID() for b in All()], Required:true}`.

- [ ] **Step 1: Write `notifier_test.go`** — construct a `NotifyPlugin` with an in-memory store + register a `fakeBackend` (id "fake", `ParseTarget` requires key `"topic"`, `Send` records calls / can be told to fail). Tests:
  - `RegisterDevice("u1", {"service":"fake","topic":"t"})` → stored, `Metadata["service"]=="fake"`, `Metadata["topic"]=="t"`.
  - `RegisterDevice` with unknown service → error.
  - `RegisterDevice` missing required `topic` → error (from ParseTarget).
  - `GetDevices(["u1"])` returns it; `GetDevices(["u2"])` empty.
  - `UpdateDevice` toggling `active:false` persists; `RevokeDevice` removes.
  - `SendNotification`: two devices (one fake-ok, one whose backend `Send` errors) → both `Send`s invoked, method returns the joined error, ok device still delivered (no early abort).
  - `NotificationSettings()` includes a `service` enum containing "fake" plus the fake's schema fields.
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement `notifier.go`; add the 7 wire names to `RPCMethods()`.**
- [ ] **Step 4: Run — expect PASS**, `go build ./src/...`, `go vet ./src/...`.
- [ ] **Step 5: Commit** `feat: Notifier RPC methods + backend dispatch`.

---

### Task 5: ntfy backend

**Files:** Create `src/backend/ntfy.go`, `src/backend/ntfy_test.go`

`ntfy` implements `Backend` and `Register(&ntfy{})` in `init()`.
- `Schema()`: fields gated `Condition:[{Key:"service",Value:"ntfy"}]` —
  `server` (String, default `https://ntfy.sh`), `topic` (String, Required), `token` (String, `Format: password`, optional).
- `ParseTarget`: `topic` required (error if empty); `server` default `https://ntfy.sh`, trim trailing `/`; pass through `token`.
- `Send`: `POST {server}/{topic}`; body = `n.Body` (fallback `n.Title`); headers: `Title: n.Title`, `Priority: PriorityScale(n.Severity,1,5)`, `Click: n.DeepLink` (if set), `Attach: n.ImageURL` (if set), `Icon: n.ImageURL` (if set), `Authorization: Bearer <token>` (if set). Non-2xx → error including status + truncated body. Use a `*http.Client` with a ~10s timeout (injectable for tests).

- [ ] **Step 1: Write `ntfy_test.go`** — `ParseTarget`: missing topic errors; server default applied; trailing slash trimmed. `Send` against `httptest.NewServer`: assert method POST, path `/<topic>`, `Title` header, `Priority` header for Info(1)/Warn/Critical(5), `Click`/`Attach` set when DeepLink/ImageURL present, `Authorization` when token set, body text. Non-2xx server → `Send` returns error.
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement `ntfy.go`.**
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** `feat: ntfy backend`.

---

### Task 6: Gotify backend

**Files:** Create `src/backend/gotify.go`, `src/backend/gotify_test.go`

- `Schema()` gated on `service=="gotify"`: `server` (String, Required), `token` (String, `Format: password`, Required, "Application token").
- `ParseTarget`: `server` + `token` both required; trim trailing `/` on server.
- `Send`: `POST {server}/message?token={token}`, JSON `{"title":n.Title, "message": n.Body||n.Title, "priority": PriorityScale(n.Severity,0,10)}`; add `"extras": {"client::display":{"contentType":"text/plain"}, "client::notification":{"click":{"url": n.DeepLink}}}` when `DeepLink` set; `"client::notification".bigImageUrl = n.ImageURL` when set. Non-2xx → error. Injectable client.

- [ ] **Step 1: Write `gotify_test.go`** — `ParseTarget` requires server+token. `Send` against `httptest`: POST path `/message`, `token` query param, JSON body title/message/priority (0..10), extras click when DeepLink set. Non-2xx errors.
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement `gotify.go`.**
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** `feat: gotify backend`.

---

### Task 7: Generic webhook backend

**Files:** Create `src/backend/webhook.go`, `src/backend/webhook_test.go`

- `Schema()` gated on `service=="webhook"`: `url` (String, Required), `method` (String, Enum `["POST","PUT"]`, default `POST`), `headerName` (String, optional), `headerValue` (String, `Format: password`, optional).
- `ParseTarget`: `url` required; default method POST; if `headerName` set, `headerValue` required (and vice-versa).
- `Send`: `{method} {url}`, `Content-Type: application/json`, optional custom header; body = JSON of `{title, subtitle, body, severity, tag, imageUrl, deepLink, data, createdAt}` (createdAt = current unix ms — inject a clock func for tests, default `time.Now`). Non-2xx → error. Injectable client.

- [ ] **Step 1: Write `webhook_test.go`** — `ParseTarget`: url required; header pairing rule. `Send` against `httptest`: method honored (POST default + PUT override), Content-Type, custom header present when configured, JSON body carries the notification fields. Non-2xx errors.
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement `webhook.go`.**
- [ ] **Step 4: Run — expect PASS** + full suite `go test ./src/...` + `go test ./src/... -race -count=1`.
- [ ] **Step 5: Commit** `feat: generic webhook backend`.

---

### Task 8: README, CHANGELOG, packaging

**Files:** Create `README.md`, `CHANGELOG.md`; verify `package.json` metadata (repo URLs → `calebcall/camera-ui-notify`).

- [ ] **Step 1:** Write `README.md` — what it is (a fully-local, multi-backend camera.ui notifier), the publisher-vs-notifier model (delivers any publisher's notifications), the three backends + their config fields + severity mapping, per-device setup in the camera.ui notifications/devices UI, build & deploy (server-native + cross-compile, mirroring the NVR README), the "adding a new backend = one file" extension note, MIT license.
- [ ] **Step 2:** Write `CHANGELOG.md` — `0.1.0`: initial release; Notifier plugin with ntfy, Gotify, generic-webhook backends; pluggable-backend registry.
- [ ] **Step 3:** `npm run bundle:dev` + `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/notify-plugin ./src/` — confirm both succeed.
- [ ] **Step 4: Commit** `docs: README + CHANGELOG for Notify plugin`.

---

## Post-plan (controller)

After Task 8: dispatch the final whole-branch code review (most-capable model) over the full diff; fix Critical/Important findings in one pass. Then deploy to the test server (`plugins/@calebcall/camera-ui-notify/`, restart, enable), register one device per backend against a local ntfy/webhook, and confirm a published test notification is delivered. Mirror progress in the SDD ledger; once the standalone repo has Issues enabled, open the epic + per-task items and back-link.
