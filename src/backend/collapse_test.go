package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

func TestSilentDelivery(t *testing.T) {
	cases := []struct {
		name  string
		notif sdk.Notification
		want  bool
	}{
		{"plain notification", sdk.Notification{Title: "Motion"}, false},
		{"silent update", sdk.Notification{Title: "Motion", Silent: true}, true},
		{"silent info", sdk.Notification{Silent: true, Severity: sdk.SeverityInfo}, true},
		{"silent warn", sdk.Notification{Silent: true, Severity: sdk.SeverityWarn}, true},
		{"silent error", sdk.Notification{Silent: true, Severity: sdk.SeverityError}, true},
		// The SDK contract is explicit that Silent does not apply to a
		// critical alert: it always makes noise.
		{"silent critical", sdk.Notification{Silent: true, Severity: sdk.SeverityCritical}, false},
		{"loud critical", sdk.Notification{Severity: sdk.SeverityCritical}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SilentDelivery(tc.notif); got != tc.want {
				t.Errorf("SilentDelivery = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReplacesTaggedMessages(t *testing.T) {
	cases := map[string]bool{
		"telegram": true,
		"discord":  true,
		"ntfy":     false,
		"gotify":   false,
		"pushover": false,
		"webhook":  false,
	}

	for id, want := range cases {
		b, ok := Get(id)
		if !ok {
			t.Fatalf("backend %q is not registered", id)
		}
		if got := ReplacesTaggedMessages(b); got != want {
			t.Errorf("ReplacesTaggedMessages(%s) = %v, want %v", id, got, want)
		}
	}
}

func TestCollapseKeyDistinguishesTargetsAndTags(t *testing.T) {
	base := collapseKey("telegram", "token", "chat", "motion:cam-1")

	if same := collapseKey("telegram", "token", "chat", "motion:cam-1"); same != base {
		t.Errorf("same inputs produced different keys: %q vs %q", same, base)
	}
	for _, other := range [][]string{
		{"telegram", "token", "chat", "motion:cam-2"},
		{"telegram", "token", "other-chat", "motion:cam-1"},
		{"telegram", "other-token", "chat", "motion:cam-1"},
		{"discord", "token", "chat", "motion:cam-1"},
	} {
		if got := collapseKey(other...); got == base {
			t.Errorf("collapseKey(%v) collided with the base key", other)
		}
	}

	// The parts routinely include a bot token or a webhook URL, both of which
	// are credentials — the key must not carry them in the clear.
	if len(base) != 64 {
		t.Errorf("key = %q, want a 64-char sha256 hex digest", base)
	}
	for _, secret := range []string{"token", "chat", "motion:cam-1"} {
		if strings.Contains(base, secret) {
			t.Errorf("key %q leaks input %q", base, secret)
		}
	}
}

func TestCollapseStoreRememberAndLookup(t *testing.T) {
	s := newCollapseStore()

	if _, ok := s.lookup("missing"); ok {
		t.Errorf("lookup of an unknown key: got ok, want miss")
	}

	s.remember("k", "42", true)
	entry, ok := s.lookup("k")
	if !ok {
		t.Fatalf("lookup after remember: got miss, want hit")
	}
	if entry.messageID != "42" {
		t.Errorf("messageID = %q, want %q", entry.messageID, "42")
	}
	if !entry.media {
		t.Errorf("photo = false, want true")
	}

	// A second delivery under the same tag supersedes the first, so a third
	// publish edits the message the second one left behind.
	s.remember("k", "43", false)
	entry, _ = s.lookup("k")
	if entry.messageID != "43" || entry.media {
		t.Errorf("entry = %+v, want messageID 43 and photo false", entry)
	}

	s.forget("k")
	if _, ok := s.lookup("k"); ok {
		t.Errorf("lookup after forget: got hit, want miss")
	}
}

func TestCollapseStoreIgnoresEmptyKeyAndID(t *testing.T) {
	s := newCollapseStore()

	s.remember("", "42", false)
	s.remember("k", "", false)

	if _, ok := s.lookup(""); ok {
		t.Errorf("lookup of the empty key: got hit, want miss")
	}
	if _, ok := s.lookup("k"); ok {
		t.Errorf("lookup after remembering an empty id: got hit, want miss")
	}
}

func TestCollapseStoreNilIsInert(t *testing.T) {
	var s *collapseStore

	// A backend constructed without newCollapseStore (as tests do) must
	// simply never collapse rather than panic.
	s.remember("k", "42", false)
	s.forget("k")
	if _, ok := s.lookup("k"); ok {
		t.Errorf("lookup on a nil store: got hit, want miss")
	}
}

func TestCollapseStoreExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newCollapseStore()
	s.now = func() time.Time { return now }
	s.ttl = time.Minute

	s.remember("k", "42", false)

	now = now.Add(59 * time.Second)
	if _, ok := s.lookup("k"); !ok {
		t.Errorf("lookup inside the TTL: got miss, want hit")
	}

	now = now.Add(2 * time.Second)
	if _, ok := s.lookup("k"); ok {
		t.Errorf("lookup past the TTL: got hit, want miss")
	}
}

// TestCollapseStoreConcurrentAccess exists for `go test -race`: each backend
// is registered once and shared process-wide, so its store is reachable from
// however many SendNotification calls the host has in flight. Sequential
// tests would let a missing lock through.
func TestCollapseStoreConcurrentAccess(t *testing.T) {
	s := newCollapseStore()
	s.max = 8 // force eviction to run under contention too

	const goroutines = 8
	const iterations = 200

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				// Keys deliberately overlap between goroutines so the same
				// entry is written and read from several at once.
				key := "tag-" + strconv.Itoa(i%16)
				s.remember(key, strconv.Itoa(g*iterations+i), i%2 == 0)
				s.lookup(key)
				if i%8 == 0 {
					s.forget(key)
				}
			}
		}(g)
	}
	wg.Wait()

	if len(s.entries) > s.max {
		t.Errorf("store holds %d entries, want at most max=%d", len(s.entries), s.max)
	}
}

// TestConcurrentSendsShareOneBackend drives the two replacing backends the
// way the host does — one registered instance, many notifications at once —
// so -race sees the real Send path, not just the store in isolation.
func TestConcurrentSendsShareOneBackend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Shaped to satisfy all three clients: Telegram reads
		// result.message_id, Discord reads id (string), Grafana annotations
		// reads id (number) — so id is sent in whichever form each parses.
		if strings.Contains(r.URL.Path, "/api/annotations") {
			_, _ = w.Write([]byte(`{"id":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"id":"1","result":{"message_id":1}}`))
	}))
	defer srv.Close()

	tg := &telegram{client: srv.Client(), baseURL: srv.URL, collapse: newCollapseStore()}
	d := &discord{client: srv.Client(), collapse: newCollapseStore()}
	gf := newGrafana()
	gf.client = srv.Client()

	tgCfg := map[string]string{"token": "tok", "chat": "99"}
	dCfg := map[string]string{"webhook": srv.URL + "/api/webhooks/1/abc"}
	gfCfg := map[string]string{
		"mode": grafanaModeAnnotations, "server": srv.URL, "token": "tk", "tags": "",
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			notif := loudNotification()
			if i%2 == 0 {
				notif = silentNotification()
			}
			// A handful of distinct tags, so some calls contend on the same
			// store entry and some don't.
			notif.Tag = "motion:cam-" + strconv.Itoa(i%3)

			if err := tg.Send(context.Background(), tgCfg, notif); err != nil {
				t.Errorf("telegram Send: %v", err)
			}
			if err := d.Send(context.Background(), dCfg, notif); err != nil {
				t.Errorf("discord Send: %v", err)
			}
			if err := gf.Send(context.Background(), gfCfg, notif); err != nil {
				t.Errorf("grafana Send: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

func TestCollapseStoreEvictsOldestAtCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newCollapseStore()
	s.now = func() time.Time { return now }
	s.ttl = time.Hour
	s.max = 3

	// Each entry is remembered a second later than the last, so "oldest" and
	// "closest to expiry" agree and eviction order is deterministic.
	for i := 0; i < 5; i++ {
		s.remember("k"+strconv.Itoa(i), strconv.Itoa(i), false)
		now = now.Add(time.Second)
	}

	if len(s.entries) > s.max {
		t.Errorf("store holds %d entries, want at most max=%d", len(s.entries), s.max)
	}
	for _, evicted := range []string{"k0", "k1"} {
		if _, ok := s.lookup(evicted); ok {
			t.Errorf("%s survived eviction, want the oldest entries dropped first", evicted)
		}
	}
	for _, kept := range []string{"k2", "k3", "k4"} {
		if _, ok := s.lookup(kept); !ok {
			t.Errorf("%s was evicted, want the newest max entries kept", kept)
		}
	}
}
