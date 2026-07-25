// Notify is a Go plugin: the actual runtime entrypoint is src/main.go,
// built directly via `go build ./src/` (see package.json's "build" script).
// This file only satisfies cameraui.config.ts's `input` field, which the
// camera.ui CLI's esbuild/TS bundling path ignores entirely when
// `language: 'go'` is set (see @camera.ui/cli's bundle command) — it exists
// purely so the config's declared entry point resolves to a real file.
export {};
