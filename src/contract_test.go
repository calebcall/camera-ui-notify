package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestContract_DeclaresNotifierInterfaceOnly proves ../contract.ts (the
// PluginContract this plugin ships, read by the closed frontend/host to
// decide which RPC handlers/UI affordances to wire up) declares
// PluginInterface.Notifier in its interfaces list, and deliberately does
// NOT declare PluginInterface.NVR, PluginInterface.OAuthCapable, or the
// PublishNotifications capability. Notify is a device-owning Notifier
// (GetDevices/RegisterDevice/SendNotification/...), not an NVR hub and not
// a notification publisher — see the NVR plugin's own contract.ts note on
// why declaring Notifier without implementing its device methods (or vice
// versa, declaring publisher capabilities on a real Notifier) breaks the
// host's NotificationManager.
//
// This package (npm run test / go test ./src/...) has no TypeScript test
// runner configured, so contract.ts's own content can't be exercised by
// importing it directly the way a Go dependency could be — this test
// instead parses the declared `interfaces` array (and the file body for
// PublishNotifications) out of the checked-in TypeScript source text, the
// same guarantee a TS-level test would give for this specific regression,
// without needing a second test toolchain for one field.
func TestContract_DeclaresNotifierInterfaceOnly(t *testing.T) {
	path := filepath.Join("..", "contract.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(data)

	interfacesMatch := regexp.MustCompile(`interfaces:\s*\[([^\]]*)\]`).FindStringSubmatch(src)
	if interfacesMatch == nil {
		t.Fatalf("could not find an `interfaces: [...]` array in %s", path)
	}
	interfacesList := interfacesMatch[1]

	if !regexp.MustCompile(`PluginInterface\.Notifier\b`).MatchString(interfacesList) {
		t.Fatalf("expected contract.ts's interfaces list to declare PluginInterface.Notifier, got: %s", interfacesList)
	}
	if regexp.MustCompile(`PluginInterface\.NVR\b`).MatchString(interfacesList) {
		t.Fatalf("expected contract.ts NOT to declare PluginInterface.NVR (Notify is not an NVR hub), got: %s", interfacesList)
	}
	if regexp.MustCompile(`PluginInterface\.OAuthCapable\b`).MatchString(interfacesList) {
		t.Fatalf("expected contract.ts NOT to declare PluginInterface.OAuthCapable (Notify has no OAuth), got: %s", interfacesList)
	}
	if regexp.MustCompile(`PluginInterface\.OAuthDeviceFlow\b|PluginInterface\.OAuthAuthCodeFlow\b|PluginInterface\.OAuthClientCredentials\b`).MatchString(interfacesList) {
		t.Fatalf("expected contract.ts NOT to declare any OAuth flow interface, got: %s", interfacesList)
	}

	capabilitiesMatch := regexp.MustCompile(`capabilities:\s*\[([^\]]*)\]`).FindStringSubmatch(src)
	if capabilitiesMatch == nil {
		t.Fatalf("could not find a `capabilities: [...]` array in %s", path)
	}
	capabilitiesList := capabilitiesMatch[1]

	if regexp.MustCompile(`PublishNotifications\b`).MatchString(capabilitiesList) {
		t.Fatalf("expected contract.ts's capabilities list NOT to declare PublishNotifications (Notify is a device-owning Notifier, not a publisher), got: %s", capabilitiesList)
	}
}
