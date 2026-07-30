package main

import (
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

func TestDiagEnabled(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]any
		want   bool
	}{
		{"missing key", map[string]any{}, false},
		{"bool true", map[string]any{"diagnostics": true}, true},
		{"bool false", map[string]any{"diagnostics": false}, false},
		{"string true", map[string]any{"diagnostics": "true"}, true},
		{"string false", map[string]any{"diagnostics": "false"}, false},
		{"unexpected type", map[string]any{"diagnostics": 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diagEnabled(&sdk.DeviceStorage{Values: tc.values})
			if got != tc.want {
				t.Fatalf("diagEnabled(%v) = %t, want %t", tc.values, got, tc.want)
			}
		})
	}
}

func TestDiagEnabled_NilStorage(t *testing.T) {
	if diagEnabled(nil) {
		t.Fatal("diagEnabled(nil) = true, want false")
	}
}
