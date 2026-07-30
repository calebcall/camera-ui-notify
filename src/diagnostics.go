// Package-level note: this file is a THROWAWAY diagnostic for issue #12. It
// measures what the closed NVR actually sends and what a Notifier plugin can
// actually observe, so the AI-descriptions design (docs/superpowers/specs/
// 2026-07-29-ai-descriptions-design.md) rests on measurements instead of
// guesses. It is deliberately not merged to main. Removal: delete this file,
// revert the two diag fields on NotifyPlugin, and drop the three call sites.
package main

import (
	sdk "github.com/cameraui/sdk/go"
)

// diagPrefix begins every diagnostic line so a whole run can be recovered with
// `grep 'notify: diag:'` and pasted into the issue.
const diagPrefix = "notify: diag:"

// diagMaxLinesPerEvent bounds diagnostic lines per detection event, so one busy
// camera cannot flood the log during an observation window.
const diagMaxLinesPerEvent = 12

// diagEnabled reports whether diagnostics are switched on. Accepts both a real
// bool and the string "true" because a value round-tripped through the host's
// config form may arrive either way, and a spike that silently stays off
// because of a type mismatch wastes an evening.
func diagEnabled(s *sdk.DeviceStorage) bool {
	if s == nil {
		return false
	}
	switch v := s.GetValue("diagnostics", false).(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}
