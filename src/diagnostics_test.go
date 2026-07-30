package main

import (
	"errors"
	"strings"
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

func TestFormatNotificationDiag_SortsDataKeysDeterministically(t *testing.T) {
	n := &sdk.Notification{
		Title:    "Person detected",
		Tag:      "motion:cam-1",
		Severity: sdk.SeverityWarn,
		Body:     "Motion in Driveway",
		DeepLink: "/cameras/cam-1",
		ImageURL: "https://example.test/snap.jpg",
		Data: map[string]string{
			"zone":     "Driveway",
			"cameraId": "cam-1",
			"eventId":  "evt-9",
		},
	}

	want := diagPrefix + ` notification: title="Person detected" tag="motion:cam-1" ` +
		`severity="warn" deepLink="/cameras/cam-1" bodyLen=18 hasThumbnail=false ` +
		`hasImageURL=true dataKeys=3 data{cameraId="cam-1" eventId="evt-9" zone="Driveway"}`

	for i := 0; i < 20; i++ {
		if got := formatNotificationDiag(n); got != want {
			t.Fatalf("iteration %d:\n got %s\nwant %s", i, got, want)
		}
	}
}

func TestFormatNotificationDiag_EmptyData(t *testing.T) {
	got := formatNotificationDiag(&sdk.Notification{Title: "Camera offline"})
	want := diagPrefix + ` notification: title="Camera offline" tag="" severity="" ` +
		`deepLink="" bodyLen=0 hasThumbnail=false hasImageURL=false dataKeys=0 data{}`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestFormatNotificationDiag_Nil(t *testing.T) {
	if got, want := formatNotificationDiag(nil), diagPrefix+" notification: <nil>"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatNotificationDiag_DoesNotMutate(t *testing.T) {
	n := &sdk.Notification{Title: "t", Body: "b", Data: map[string]string{"cameraId": "cam-1"}}
	_ = formatNotificationDiag(n)
	if n.Title != "t" || n.Body != "b" || len(n.Data) != 1 || n.Data["cameraId"] != "cam-1" {
		t.Fatalf("formatNotificationDiag mutated its argument: %+v", n)
	}
}

func TestFormatCamerasDiag_None(t *testing.T) {
	got := formatCamerasDiag(nil)
	want := diagPrefix + " configureCameras: 0 cameras assigned to Notify"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatCamerasDiag_Several(t *testing.T) {
	got := formatCamerasDiag([]diagCamera{
		{ID: "cam-1", Name: "Front Door"},
		{ID: "cam-2", Name: "Driveway"},
	})
	want := diagPrefix + ` configureCameras: 2 camera(s) assigned: cam-1("Front Door") cam-2("Driveway")`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestToDiagCameras_SkipsNil(t *testing.T) {
	got := toDiagCameras([]*sdk.CameraDevice{nil, {}, nil})
	if len(got) != 1 {
		t.Fatalf("toDiagCameras kept %d entries, want 1 (nils dropped)", len(got))
	}
}

func TestFormatProbeDiag(t *testing.T) {
	tests := []struct {
		name  string
		found bool
		cname string
		err   error
		want  string
	}{
		{
			name: "error",
			err:  errors.New("not permitted"),
			want: diagPrefix + ` getCamera("cam-1" via cameraId): ERROR not permitted -- inconclusive: could be visibility, or a device-init failure unrelated to visibility`,
		},
		{
			name: "nil result",
			want: diagPrefix + ` getCamera("cam-1" via cameraId): nil (no error) -- camera was not returned to this plugin`,
		},
		{
			name:  "found",
			found: true,
			cname: "Front Door",
			want: diagPrefix + ` getCamera("cam-1" via cameraId): OK name="Front Door" -- reachable, but this may be a ` +
				`ConfigureCameras cache hit rather than proof this camera is unassigned; cross-check against question 2's assigned-camera list`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatProbeDiag("cam-1", "cameraId", tc.found, tc.cname, tc.err)
			if got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestFormatProbeDiag_ReportsMatchedKey(t *testing.T) {
	got := formatProbeDiag("cam-1", "camera", true, "Front Door", nil)
	if !strings.Contains(got, "via camera") {
		t.Fatalf("want the matched key spelling reported, got %s", got)
	}
}

func TestFindDiagCameraID(t *testing.T) {
	tests := []struct {
		name            string
		data            map[string]string
		wantID, wantKey string
		wantOK          bool
	}{
		{"cameraId present", map[string]string{"cameraId": "cam-1"}, "cam-1", "cameraId", true},
		{"cameraID fallback", map[string]string{"cameraID": "cam-2"}, "cam-2", "cameraID", true},
		{"camera fallback", map[string]string{"camera": "cam-3"}, "cam-3", "camera", true},
		{"prefers cameraId over other spellings", map[string]string{"cameraId": "cam-1", "camera": "cam-3"}, "cam-1", "cameraId", true},
		{"prefers cameraID over camera", map[string]string{"cameraID": "cam-2", "camera": "cam-3"}, "cam-2", "cameraID", true},
		{"none present", map[string]string{"zone": "Driveway"}, "", "", false},
		{"blank value not matched", map[string]string{"cameraId": ""}, "", "", false},
		{"nil map", nil, "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, key, ok := findDiagCameraID(tc.data)
			if id != tc.wantID || key != tc.wantKey || ok != tc.wantOK {
				t.Fatalf("findDiagCameraID(%v) = (%q, %q, %t), want (%q, %q, %t)",
					tc.data, id, key, ok, tc.wantID, tc.wantKey, tc.wantOK)
			}
		})
	}
}

func TestFormatNoCameraIDDiag_NamesTriedKeys(t *testing.T) {
	got := formatNoCameraIDDiag([]string{"cameraId", "cameraID", "camera"})
	want := diagPrefix + ` getCamera: no camera-id key found in notification Data (tried: cameraId, cameraID, camera)`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestFormatDetectionDiag_NoDescription(t *testing.T) {
	ev := sdk.DetectionEvent{
		ID: "evt-1", CameraID: "cam-1", State: sdk.DetectionEventStateActive,
		StartTime:    1000,
		Segments:     []sdk.EventSegment{{}},
		SegmentIndex: 0,
	}
	got := formatDetectionDiag(sdk.DetectionEventSegmentStart, ev, 4000)
	want := diagPrefix + ` detectionEvent: type=segment-start eventId="evt-1" cameraId="cam-1" ` +
		`state="active" msgSegments=1 segmentIndex=0 withSummary=[] elapsedMs=3000`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestFormatDetectionDiag_ReportsSegmentsCarryingSummary(t *testing.T) {
	ev := sdk.DetectionEvent{
		ID: "evt-2", CameraID: "cam-1", State: sdk.DetectionEventStateActive,
		StartTime:    1000,
		SegmentIndex: 4,
		Segments: []sdk.EventSegment{
			{},
			{Description: &sdk.EventDescription{Summary: "A man approached the door."}},
			{Description: &sdk.EventDescription{Summary: "   "}},
		},
	}
	got := formatDetectionDiag(sdk.DetectionEventSegmentUpdate, ev, 61000)
	want := diagPrefix + ` detectionEvent: type=segment-update eventId="evt-2" cameraId="cam-1" ` +
		`state="active" msgSegments=3 segmentIndex=4 withSummary=[1] elapsedMs=60000`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestSegmentHasSummary(t *testing.T) {
	tests := []struct {
		name string
		seg  sdk.EventSegment
		want bool
	}{
		{"nil description", sdk.EventSegment{}, false},
		{"blank summary", sdk.EventSegment{Description: &sdk.EventDescription{Summary: "   "}}, false},
		{"empty summary", sdk.EventSegment{Description: &sdk.EventDescription{Summary: ""}}, false},
		{"non-blank summary", sdk.EventSegment{Description: &sdk.EventDescription{Summary: "A cat crossed the yard."}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := segmentHasSummary(tc.seg); got != tc.want {
				t.Fatalf("segmentHasSummary(%+v) = %t, want %t", tc.seg, got, tc.want)
			}
		})
	}
}

func TestEventHasSummary(t *testing.T) {
	tests := []struct {
		name string
		ev   sdk.DetectionEvent
		want bool
	}{
		{"no segments", sdk.DetectionEvent{}, false},
		{"only blank summaries", sdk.DetectionEvent{Segments: []sdk.EventSegment{
			{}, {Description: &sdk.EventDescription{Summary: "  "}},
		}}, false},
		{"one non-blank summary among several", sdk.DetectionEvent{Segments: []sdk.EventSegment{
			{}, {Description: &sdk.EventDescription{Summary: "A man approached."}},
		}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventHasSummary(tc.ev); got != tc.want {
				t.Fatalf("eventHasSummary(%+v) = %t, want %t", tc.ev, got, tc.want)
			}
		})
	}
}

func TestFormatDetectionBudgetExhaustedDiag(t *testing.T) {
	got := formatDetectionBudgetExhaustedDiag("evt-1", 12)
	want := diagPrefix + ` detectionEvent: eventId="evt-1" budget exhausted (cap=12), suppressing further non-summary messages for this event`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestFormatSubscribeDiag(t *testing.T) {
	got := formatSubscribeDiag("cam-1")
	want := diagPrefix + ` subscribe: camera="cam-1" ok`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatDetectionDiag_UnknownStartTime(t *testing.T) {
	ev := sdk.DetectionEvent{ID: "evt-3", CameraID: "cam-1"}
	got := formatDetectionDiag(sdk.DetectionEventEnd, ev, 5000)
	if !strings.Contains(got, "elapsedMs=-1") {
		t.Fatalf("want elapsedMs=-1 when StartTime is unset, got %s", got)
	}
}

func TestDiagLogCap_AllowsUpToLimitPerEvent(t *testing.T) {
	c := newDiagLogCap(3)
	for i := 1; i <= 3; i++ {
		if !c.allow("evt-1") {
			t.Fatalf("call %d for evt-1 denied, want allowed", i)
		}
	}
	if c.allow("evt-1") {
		t.Fatal("call 4 for evt-1 allowed, want denied")
	}
	if !c.allow("evt-2") {
		t.Fatal("a different event should have its own budget")
	}
}

func TestDiagLogCap_ZeroLimitDeniesEverything(t *testing.T) {
	if newDiagLogCap(0).allow("evt-1") {
		t.Fatal("zero limit allowed a line, want denied")
	}
}

func TestDiagLogCap_Decide_SummaryAlwaysLogsAndNeverConsumesBudget(t *testing.T) {
	c := newDiagLogCap(1)
	for i := 0; i < 5; i++ {
		if got := c.decide("evt-1", true); got != diagGateLogMessage {
			t.Fatalf("summary call %d = %v, want diagGateLogMessage", i, got)
		}
	}
	// The budget for non-summary messages must be untouched: five summary
	// calls above must not have consumed the single slot.
	if got := c.decide("evt-1", false); got != diagGateLogMessage {
		t.Fatalf("first non-summary call after 5 summary calls = %v, want diagGateLogMessage (budget must be untouched)", got)
	}
}

func TestDiagLogCap_Decide_ExhaustionNoticeFiresExactlyOnce(t *testing.T) {
	c := newDiagLogCap(2)
	if got := c.decide("evt-1", false); got != diagGateLogMessage {
		t.Fatalf("call 1 = %v, want diagGateLogMessage", got)
	}
	if got := c.decide("evt-1", false); got != diagGateLogMessage {
		t.Fatalf("call 2 = %v, want diagGateLogMessage", got)
	}
	if got := c.decide("evt-1", false); got != diagGateLogNotice {
		t.Fatalf("call 3 (first over budget) = %v, want diagGateLogNotice", got)
	}
	if got := c.decide("evt-1", false); got != diagGateSuppress {
		t.Fatalf("call 4 = %v, want diagGateSuppress (notice already fired)", got)
	}
	if got := c.decide("evt-1", false); got != diagGateSuppress {
		t.Fatalf("call 5 = %v, want diagGateSuppress (notice already fired)", got)
	}
}

func TestDiagLogCap_Decide_SummaryLoggedEvenAfterBudgetExhausted(t *testing.T) {
	c := newDiagLogCap(1)
	c.decide("evt-1", false) // consumes the only slot
	c.decide("evt-1", false) // exhausted: fires the notice
	if got := c.decide("evt-1", true); got != diagGateLogMessage {
		t.Fatalf("summary message after exhaustion = %v, want diagGateLogMessage: this is the one answer the spike must never drop", got)
	}
}

func TestDiagLogCap_Decide_IndependentBudgetsPerEvent(t *testing.T) {
	c := newDiagLogCap(1)
	c.decide("evt-1", false)
	c.decide("evt-1", false) // evt-1 exhausted
	if got := c.decide("evt-2", false); got != diagGateLogMessage {
		t.Fatalf("evt-2 first call = %v, want diagGateLogMessage (independent budget from evt-1)", got)
	}
}

// fakeDisposer records whether Dispose was called, standing in for
// *sdk.Disposable (which the registry only ever calls Dispose on).
type fakeDisposer struct{ disposed int }

func (f *fakeDisposer) Dispose() { f.disposed++ }

func TestDiagSubscriptions_AddIsIdempotentPerCamera(t *testing.T) {
	s := newDiagSubscriptions()
	first, second := &fakeDisposer{}, &fakeDisposer{}

	if !s.add("cam-1", first) {
		t.Fatal("first add for cam-1 returned false, want true")
	}
	if s.add("cam-1", second) {
		t.Fatal("second add for cam-1 returned true, want false (already subscribed)")
	}
	if s.count() != 1 {
		t.Fatalf("count = %d, want 1", s.count())
	}
	if second.disposed != 1 {
		t.Fatalf("rejected duplicate was disposed %d times, want 1 (must not leak)", second.disposed)
	}
}

func TestDiagSubscriptions_DisposeAll(t *testing.T) {
	s := newDiagSubscriptions()
	a, b := &fakeDisposer{}, &fakeDisposer{}
	s.add("cam-1", a)
	s.add("cam-2", b)

	s.disposeAll()

	if a.disposed != 1 || b.disposed != 1 {
		t.Fatalf("disposed counts = %d/%d, want 1/1", a.disposed, b.disposed)
	}
	if s.count() != 0 {
		t.Fatalf("count after disposeAll = %d, want 0", s.count())
	}
	s.disposeAll() // must be safe twice
	if a.disposed != 1 {
		t.Fatalf("second disposeAll re-disposed: %d, want 1", a.disposed)
	}
}

func TestDiagSubscriptions_IgnoresNil(t *testing.T) {
	s := newDiagSubscriptions()
	if s.add("cam-1", nil) {
		t.Fatal("add with nil disposer returned true, want false")
	}
	if s.count() != 0 {
		t.Fatalf("count = %d, want 0", s.count())
	}
}
