# Revision: config-driven single target (camera-ui-notify)

**Why:** the stock camera.ui UI has NO way to *create* a notifier device — `registerDevice` is only ever called by the mobile app's `usePushRegistration`, hardcoded to plugin name `@camera.ui/camera-ui-nvr`. `notificationSettings()` is rendered display-only in the "send test" panel. So our device-registration model is unreachable. The generic plugin page (`views/Plugin.vue`) DOES render a plugin's `StorageSchema` config as an editable+savable form (`CuiSchema` → `setConfig`). Fix: the notify plugin holds ONE target in its own `StorageSchema` config; `getDevices()` synthesizes the device from it. No registerDevice, no mobile app, no hardcoded name.

**Scope:** one code change + tests + docs. Backends (ntfy/gotify/webhook) and the `Send` dispatch are UNCHANGED. Branch `feat/notify-config-targets` off main.

## R1 — code pivot (src/plugin.go, src/notifier.go; delete src/store.go + store_test.go; update tests)

1. **StorageSchema() becomes the target config form.** Return:
   - a `service` field: `{Key:"service", Type:String, Title:"Service", Enum: [b.ID() for b in backend.All()], Store:&true}` (no Required — empty = "not configured yet").
   - PLUS every backend's `Schema()` fields flattened (`for b in backend.All(): append(b.Schema()...)`), but with `Store:&true` set on each (they're persisted config now). The fields are already namespaced (`ntfy_server`, `gotify_token`, …) and `Condition`-gated on `service`, so the plugin page shows only the selected service's fields. (Set Store on a COPY so you don't mutate the backend's returned schema across calls.)
   - Drop the old `devices` key entirely.
2. **getDevices(ownerUserIDs) synthesizes from config:**
   - Read `service := storage.GetValue("service","")`. If empty → return `[]sdk.NotifierDevice{}` (nothing configured).
   - `b, ok := backend.Get(service)`; if !ok → return `[]`.
   - Build an `input map[string]any` from the plugin's stored config for that service's namespaced keys (read each via `storage.GetValue(key)` for the keys in `b.Schema()`), then `cfg, err := b.ParseTarget(input)`; if err (incomplete config) → return `[]` (treat as not-yet-configured; optionally log at debug).
   - Return ONE device: `{ID: "cfg:"+service (stable), OwnerUserID: <see ownership note>, Name: b.Label(), Active: true, Metadata: merge({"service":service}, cfg as map[string]any with STRING values only)}`.
   - **Ownership note:** the host's `notify()` filters devices by `d.OwnerUserID ∈ ownerUserIDs` (the eligible users). To deliver the instance-global target exactly once: if `len(ownerUserIDs) > 0`, set `OwnerUserID = ownerUserIDs[0]`; if empty (admin UI listing), set `OwnerUserID = ""`. `notify()` never calls getDevices with an empty eligible set (it returns earlier), so [0] is always safe and yields a single delivery. Document this in a comment.
3. **getDevice(id):** return the synthesized device if `id == "cfg:"+service`, else `(nil,nil)`.
4. **sendNotification:** UNCHANGED (reads device.Metadata["service"] → backend.Get → cfg = metadata minus service → b.Send). Confirm it still works with synthesized devices.
5. **registerDevice / updateDevice / revokeDevice:** devices are config-driven, not registered. Make them safe no-ops: `registerDevice` returns an error like `"notify targets are configured in the plugin settings, not registered"`; `updateDevice` returns `(nil, nil)`; `revokeDevice` returns `nil`. (The stock UI never calls registerDevice for this plugin; these exist only to satisfy the interface.)
6. **notificationSettings():** return `(nil, nil)` — the config now lives on the plugin's config tab (StorageSchema); duplicating it in the test panel would confuse. The test panel still shows the synthesized device as a target via getDevices.
7. **Delete `src/store.go` + `src/store_test.go`** (the device store is replaced by config). Remove the `devicesStorageKey` const and any store wiring in plugin.go/NewPlugin.
8. **Keep** the `var _ sdk.NotifierInterface = (*NotifyPlugin)(nil)` assertion (all 7 methods still implemented).

**Tests (src/notifier_test.go — rewrite the device-model tests):** use a fake DeviceStorage seeded with config values.
- Unconfigured (no `service`) → getDevices returns empty.
- Configured ntfy (`service`=ntfy, `ntfy_server`,`ntfy_topic`) → getDevices returns ONE device, Metadata has `service`+short cfg keys (server/topic), OwnerUserID == ownerUserIDs[0].
- Incomplete config (service set, required field missing) → getDevices returns empty (ParseTarget error swallowed).
- getDevice("cfg:ntfy") returns it; getDevice("other") → nil.
- sendNotification([synthesized id], n) dispatches to the right backend (use a fake backend registered in the test + a fake httptest? — reuse the existing pattern: a fake backend recording Send calls).
- registerDevice returns an error; updateDevice/revokeDevice are benign.
- StorageSchema() includes the `service` enum + a selected backend's namespaced fields, all with Store:true.

**Verify:** `cd <repo> && go build ./src/... && go vet ./src/... && go test ./src/... -race -count=2` all clean. Commit (no Co-Authored-By/Claude/Generated-with).

## R2 — docs
Update README + CHANGELOG: targets are configured in the plugin's settings page (single active target: pick Service + fill fields), not "added as devices". CHANGELOG `## [0.2.0]` (breaking model change) or amend 0.1.0 since unreleased — use 0.2.0.

## Post
Re-review the pivot diff; redeploy to the server; then USER-verifiable: open the Notify plugin's page in camera.ui, set Service=ntfy + a throwaway ntfy.sh topic, save; trigger a person detection; confirm delivery at https://ntfy.sh/<topic>.
