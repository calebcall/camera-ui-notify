package backend

import (
	"net/url"
	"strings"

	sdk "github.com/cameraui/sdk/go"
)

// DeepLinkLabelCamera and DeepLinkLabelOther are the two call-to-action
// labels a backend may put on a notification's tap-through link. Only
// backends whose API carries label text alongside the URL need them
// (Telegram's inline-keyboard button, Pushover's url_title); ntfy's Click
// header, Gotify's click.url, Discord's embed.url and the generic webhook all
// carry the URL alone.
const (
	// DeepLinkLabelCamera is used when the link opens a single camera's page.
	DeepLinkLabelCamera = "Open camera"
	// DeepLinkLabelOther is used for every other destination — plugin
	// updates, system/operational alerts, settings pages — where "Open
	// camera" would misdescribe where the link goes.
	DeepLinkLabelOther = "Open in camera.ui"
)

// DeepLinkLabel returns the call-to-action text for notif's DeepLink.
//
// The label describes where the link actually goes, so the destination path
// decides: a single-camera route (a "cameras" segment followed by a camera
// id, in either the router-relative form camera.ui publishes or the absolute
// form produced when base_url is configured — including a sub-path mount)
// gets DeepLinkLabelCamera, and any other identifiable destination gets
// DeepLinkLabelOther. Data["cameraId"] is consulted only when the path names
// no destination at all, so a camera-scoped operational alert that links to,
// say, a settings page is still labelled for the page it opens.
func DeepLinkLabel(notif sdk.Notification) string {
	path := deepLinkPath(notif.DeepLink)
	switch {
	case pathTargetsCamera(path):
		return DeepLinkLabelCamera
	case path != "" && path != "/":
		return DeepLinkLabelOther
	case notif.Data["cameraId"] != "":
		return DeepLinkLabelCamera
	default:
		return DeepLinkLabelOther
	}
}

// deepLinkPath extracts the path portion of a deep link, which may be either
// router-relative ("/cameras/cam-1?startTs=…") or absolute
// ("https://host/cameras/cam-1"). An unparseable value yields "", which
// DeepLinkLabel treats as "no destination named".
func deepLinkPath(deepLink string) string {
	if deepLink == "" {
		return ""
	}
	u, err := url.Parse(deepLink)
	if err != nil {
		return ""
	}
	return u.Path
}

// pathTargetsCamera reports whether path addresses one specific camera, i.e.
// contains a "cameras" segment followed by a non-empty id segment. Matching a
// segment rather than a prefix keeps this correct when camera.ui is mounted
// under a sub-path ("/camera-ui/cameras/cam-1"), and requiring the trailing
// id keeps the camera *list* page ("/cameras") out.
func pathTargetsCamera(path string) bool {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		if seg == "cameras" {
			return i+1 < len(segments) && segments[i+1] != ""
		}
	}
	return false
}
