# Publishing

Releases are built and published to npm by the `publish` GitHub Action
(`.github/workflows/publish.yml`) when a version tag is pushed. Auth uses npm
**Trusted Publishing (OIDC)** — no `NPM_TOKEN` is stored, so there is nothing to
rotate.

## One-time setup (npmjs.com)

Trusted Publishing is configured **per package**. This plugin publishes the main
package plus one package per platform target, so all of the following must have a
trusted publisher pointing at this repo + workflow:

- `@calebcall/camera-ui-notify` (main)
- `@calebcall/camera-ui-notify-darwin-arm64`
- `@calebcall/camera-ui-notify-darwin-amd64`
- `@calebcall/camera-ui-notify-linux-amd64`
- `@calebcall/camera-ui-notify-linux-arm64`
- `@calebcall/camera-ui-notify-windows-amd64`
- `@calebcall/camera-ui-notify-windows-arm64`
- `@calebcall/camera-ui-notify-linux-amd64-musl`
- `@calebcall/camera-ui-notify-linux-arm64-musl`

Each package must already exist on npm (publish once with a token first — done for
0.4.0). Then for **each** package, on npmjs.com:

1. Open the package → **Settings** → **Trusted Publisher** (a.k.a. Publishing
   access → GitHub Actions).
2. Set:
   - **Organization / user:** `calebcall`
   - **Repository:** `camera-ui-notify`
   - **Workflow filename:** `publish.yml`
   - **Environment:** leave blank (the workflow uses none).
3. Save.

Once all nine are configured, publishing needs no token — the workflow's OIDC
identity is trusted, and npm records a provenance attestation for each package.

## Cutting a release

1. Bump `version` in `package.json` and add a matching `## [<version>]` entry to
   `CHANGELOG.md`. **Required** — the workflow fails the release if the changelog
   has no entry for the version being published.
2. Commit to `main`.
3. Tag and push:
   ```bash
   git tag v0.5.0     # must match package.json version exactly
   git push origin v0.5.0
   ```
4. The `publish` workflow builds all platforms and publishes them to npm under
   `latest`. (It fails fast if the tag doesn't match `package.json` version.)

## Where `optionalDependencies` comes from

The published package carries an `optionalDependencies` block naming the eight
per-platform packages, so npm installs only the binary matching the host's
`os`/`cpu`/`libc`. That block lives **only** in `bundle/package.json`, which
`cui bundle` regenerates from the Go targets in `cameraui.config.ts` on every run.
The repo's own `package.json` must not contain it: the entries would pin a version
that doesn't exist on npm until the release publishes it, so `package-lock.json`
could never resolve them and `npm ci` would fail with
`Missing: @calebcall/camera-ui-notify-<platform>@ from lock file` — breaking the
`publish` workflow at its "Install dependencies" step.

`@camera.ui/cli` 0.0.73 and 0.0.74 wrote that block into the repo's `package.json`
on every bundle, which caused exactly that failure (#16, reported upstream as
[cameraui/cli#31](https://github.com/cameraui/cli/issues/31)). **Fixed in
`@camera.ui/cli` 0.0.75** — `cui bundle` no longer touches the repo's
`package.json`, so no cleanup step is needed. If a future CLI regresses this, the
symptom is a dirty `package.json` after `npm run bundle`; discard it with
`git checkout -- package.json` and don't commit it.

## Dry run (manual)

To exercise the build/cross-compile/bundle path **without publishing** (e.g. to
validate the pipeline before the trusted publishers are configured, or before
cutting a real release): GitHub → **Actions** → **publish** → **Run workflow**.
The `dry_run` box is **checked by default**, so a manual run builds everything
and stops before `npm publish`. Uncheck it to publish `package.json`'s current
version manually via the same OIDC path (no tag required).

## Notes

- The workflow upgrades npm to the latest before publishing — Trusted Publishing
  needs npm ≥ 11.5.1, and the Node 22 runner ships npm 10.x.
- To publish a pre-release instead of `latest`, adjust the `cui publish` flag in
  the workflow (`--alpha` / `--beta`), or add a separate workflow keyed off a
  pre-release tag pattern.
- Local/manual publishing still works via `npm run publish:latest` if you have an
  npm token in `~/.npmrc`, but the Action is the token-free path.
