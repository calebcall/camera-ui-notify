package backend

import (
	"context"
	"strconv"
	"strings"

	sdk "github.com/cameraui/sdk/go"
)

// VideoLinkLabel is the call-to-action text every backend puts on a
// notification's clip link, wherever its API has room for one (an ntfy
// action button, a Telegram inline-keyboard button, a Pushover
// supplementary url, a Discord/Grafana anchor). Shared so the wording cannot
// drift between backends, the same way DeepLinkLabel is.
const VideoLinkLabel = "Play clip"

// VideoLink returns notif's clip URL when it is usable as a link, or "".
//
// camera.ui 2.1.6 added Notification.VideoURL: a short MP4 of the recording
// that triggered the alert, published when the camera (or the episode) has
// "Video in Push" switched on. Only the first-party mobile app renders it
// inline as a push attachment; every backend this plugin talks to is a
// third-party service that, at best, can carry a link to it. So each backend
// surfaces the URL in whatever slot it has — none of them replace the
// snapshot with it, which is exactly what the SDK asks for ("everything else
// ignores it, so always send the image alongside").
//
// The http prefix check mirrors the one the DeepLink-carrying backends
// already apply: camera.ui publishes VideoURL absolute, but a router-relative
// value (or a plugin-relative one that SendNotification could not absolutize
// because base_url is unset) is not something a Telegram button or a Discord
// embed can open, so it is dropped rather than delivered as a dead link.
func VideoLink(notif sdk.Notification) string {
	if !strings.HasPrefix(notif.VideoURL, "http") {
		return ""
	}
	return notif.VideoURL
}

// WithoutVideoLink returns a copy of notif with VideoURL cleared.
//
// A backend that is uploading the clip itself passes its notification
// through this before rendering, so the shared link helpers leave out the
// "Play clip" link that would now point at a file already sitting in the
// message. sdk.Notification is taken and returned by value, so the caller's
// notification is untouched.
func WithoutVideoLink(notif sdk.Notification) sdk.Notification {
	notif.VideoURL = ""
	return notif
}

// ClipUploader is implemented by backends that can put the clip's bytes into
// the message itself, rather than only linking to it — Telegram's sendVideo
// and a Discord file attachment both render a real player.
//
// The plugin, not the backend, fetches those bytes: it owns the HTTP client
// and the logger, so a slow or oversized clip is reported once, in one place,
// instead of failing silently inside a backend that has no way to say so. The
// backend's job is to declare what it will accept and then upload what it is
// handed.
//
// This is opt-in per target (see each backend's "Upload video clips" field)
// because it is the expensive path: every clip is downloaded from camera.ui
// and re-uploaded to the delivery service, where the default behaviour merely
// passes a URL along.
type ClipUploader interface {
	// ClipLimit returns the largest clip, in bytes, this backend will upload
	// for notif under cfg — or 0 for "don't fetch anything", which covers
	// both a target that hasn't opted in and a notification whose delivery
	// won't need the bytes.
	ClipLimit(cfg map[string]string, notif sdk.Notification) int
	// SendWithClip delivers notif with clip, already fetched and within the
	// limit ClipLimit returned, embedded as a media attachment. An error
	// means nothing was delivered: the caller retries through Send, which
	// falls back to linking the clip.
	SendWithClip(ctx context.Context, cfg map[string]string, notif sdk.Notification, clip []byte) error
}

// ClipUpload reports whether notif's clip should be fetched and uploaded to b
// under cfg, returning the uploader and its size limit, or (nil, 0).
//
// It is the single gate for the upload path: the backend must implement
// ClipUploader, cfg must have opted in (which is what a non-zero ClipLimit
// means), and the notification must actually carry a fetchable clip.
func ClipUpload(b Backend, cfg map[string]string, notif sdk.Notification) (ClipUploader, int) {
	u, ok := b.(ClipUploader)
	if !ok || VideoLink(notif) == "" {
		return nil, 0
	}
	limit := u.ClipLimit(cfg, notif)
	if limit <= 0 {
		return nil, 0
	}
	return u, limit
}

// clipOptIn is the cfg value a backend stores for an enabled "Upload video
// clips" toggle. Spelled out as a constant because ParseTarget writes it and
// ClipLimit reads it back, in different files.
const clipOptIn = "true"

// parseClipOptIn reads a backend's "upload video clips" toggle out of raw
// registration input.
//
// The plugin's config form declares the field as JsonSchemaTypeBoolean, so a
// saved value arrives as a bool — but an unset field arrives as the ""
// that configuredDevice passes for every field it has no stored value for,
// and a value round-tripped through some other storage path could arrive as a
// string. All three are handled here, defaulting to off: uploading clips is
// the surprising, expensive behaviour, so only an explicit yes enables it.
func parseClipOptIn(input map[string]any, key string) bool {
	switch v := input[key].(type) {
	case bool:
		return v
	case string:
		enabled, err := strconv.ParseBool(strings.TrimSpace(v))
		return err == nil && enabled
	default:
		return false
	}
}

// clipUploadSchema builds the per-backend "Upload video clips" toggle. The
// wording differs per backend (what the trade-off is, and how big a clip may
// be), so the caller supplies the description.
func clipUploadSchema(key, description string, cond []sdk.SchemaCondition) sdk.JsonSchema {
	return sdk.JsonSchema{
		Type:         sdk.JsonSchemaTypeBoolean,
		Key:          key,
		Title:        "Upload video clips",
		Description:  description,
		DefaultValue: false,
		Condition:    cond,
	}
}
