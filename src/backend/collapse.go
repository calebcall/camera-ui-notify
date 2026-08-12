package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// SilentDelivery reports whether notif is an update-only publish that must
// not alert again.
//
// camera.ui republishes a notification under the same Tag when it has more to
// say about an event it already announced — most commonly the AI-generated
// description arriving a few seconds after the initial detection alert. That
// second publish carries Silent, meaning "this supersedes the earlier one,
// don't buzz the user twice". Per the SDK contract, Silent is ignored for
// SeverityCritical: a critical alert always makes noise.
func SilentDelivery(notif sdk.Notification) bool {
	return notif.Silent && notif.Severity != sdk.SeverityCritical
}

// TagReplacer is implemented by backends whose API can edit an
// already-delivered message, so a republish under the same Notification.Tag
// updates the original in place instead of adding a second entry. Backends
// that can't (ntfy, Gotify, Pushover, a generic webhook) simply don't
// implement it.
type TagReplacer interface {
	// ReplacesTaggedMessages reports that this backend replaces by tag. It is
	// a method rather than a marker so a backend could later make replacement
	// conditional on its own config.
	ReplacesTaggedMessages() bool
}

// ReplacesTaggedMessages reports whether b replaces a previously delivered
// message when a notification repeats its Tag. Callers use it to decide
// whether a silent update-only publish is worth delivering: on a replacing
// backend it costs the user nothing (the original message just changes),
// while on the others it is necessarily one more entry in the list.
func ReplacesTaggedMessages(b Backend) bool {
	r, ok := b.(TagReplacer)
	return ok && r.ReplacesTaggedMessages()
}

const (
	// collapseTTL bounds how long a delivered message stays eligible for
	// in-place replacement. The AI description follows its detection alert
	// within seconds, so this only has to outlive a slow description run;
	// past that, editing an hours-old message would be more surprising than
	// posting a new one.
	collapseTTL = 15 * time.Minute
	// collapseMaxEntries caps the store so a busy instance with many cameras
	// can't grow it without bound between expiries.
	collapseMaxEntries = 512
)

// collapseEntry records one delivered message that a later same-tag publish
// may replace.
type collapseEntry struct {
	// messageID is the platform's own id for the delivered message (Telegram
	// message_id, Discord message id), as the edit endpoint expects it.
	messageID string
	// media records whether the delivered message carried an image or a
	// video. Telegram needs this to pick between editMessageText and
	// editMessageCaption; a message posted with media can never be edited as
	// text, and both sendPhoto and sendVideo produce one.
	media bool
	// expiresAt is when this entry stops being eligible for replacement.
	expiresAt time.Time
}

// collapseStore maps a notification tag to the message previously delivered
// under it, so a backend whose API can edit a sent message replaces that
// message in place instead of posting a duplicate.
//
// Deliberately in-memory and best-effort: the mapping is worthless across a
// plugin restart anyway (nothing else in the plugin is persisted per-event),
// and every caller treats a miss as "just send a new message". A nil
// *collapseStore is a valid store that never remembers anything, which is how
// tests that construct a backend struct directly opt out of collapsing.
type collapseStore struct {
	mu      sync.Mutex
	entries map[string]collapseEntry

	// now returns the current time. Defaulted by newCollapseStore to
	// time.Now; overridable in tests to drive expiry deterministically.
	now func() time.Time
	// ttl and max mirror collapseTTL / collapseMaxEntries; fields rather than
	// constants so tests can shrink them.
	ttl time.Duration
	max int
}

// newCollapseStore builds a store with production TTL/size limits and a real
// clock.
func newCollapseStore() *collapseStore {
	return &collapseStore{
		entries: map[string]collapseEntry{},
		now:     time.Now,
		ttl:     collapseTTL,
		max:     collapseMaxEntries,
	}
}

// collapseKey derives the store key for one tag on one delivery target. The
// parts identify the target (bot token + chat, webhook URL, ...) so the same
// tag delivered to two different chats never collides, and they are hashed
// rather than concatenated so credentials are not held as map keys — several
// of these targets are themselves secrets.
func collapseKey(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// lookup returns the message recorded under key, if one is present and not
// yet expired.
func (s *collapseStore) lookup(key string) (collapseEntry, bool) {
	if s == nil || key == "" {
		return collapseEntry{}, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return collapseEntry{}, false
	}
	if !s.clock().Before(entry.expiresAt) {
		delete(s.entries, key)
		return collapseEntry{}, false
	}
	return entry, true
}

// remember records messageID as the message now occupying key, replacing any
// earlier entry so a third publish under the same tag edits the message the
// second one left behind.
func (s *collapseStore) remember(key, messageID string, media bool) {
	if s == nil || key == "" || messageID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.entries == nil {
		s.entries = map[string]collapseEntry{}
	}
	s.prune()
	s.entries[key] = collapseEntry{
		messageID: messageID,
		media:     media,
		expiresAt: s.clock().Add(s.ttl),
	}
}

// forget drops key, used when an edit is rejected (the message was deleted,
// the token was rotated, ...) so the next publish starts clean.
func (s *collapseStore) forget(key string) {
	if s == nil || key == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.entries, key)
}

// prune drops expired entries and, if the store is still at capacity, the
// entry closest to expiry — which under a uniform TTL is the oldest. Callers
// must hold s.mu.
func (s *collapseStore) prune() {
	now := s.clock()
	for k, e := range s.entries {
		if !now.Before(e.expiresAt) {
			delete(s.entries, k)
		}
	}

	max := s.max
	if max <= 0 {
		max = collapseMaxEntries
	}
	for len(s.entries) >= max {
		oldestKey := ""
		var oldest time.Time
		for k, e := range s.entries {
			if oldestKey == "" || e.expiresAt.Before(oldest) {
				oldestKey, oldest = k, e.expiresAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(s.entries, oldestKey)
	}
}

// clock reads the store's time source, falling back to the real clock for a
// zero-valued store. Callers must hold s.mu.
func (s *collapseStore) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}
