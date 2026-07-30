package main

import (
	"errors"
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
			want: diagPrefix + ` getCamera("cam-1"): ERROR not permitted -- unassigned cameras are NOT reachable`,
		},
		{
			name: "nil result",
			want: diagPrefix + ` getCamera("cam-1"): nil (no error) -- camera not visible to this plugin`,
		},
		{
			name:  "found",
			found: true,
			cname: "Front Door",
			want:  diagPrefix + ` getCamera("cam-1"): OK name="Front Door" -- unassigned cameras ARE reachable`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatProbeDiag("cam-1", tc.found, tc.cname, tc.err)
			if got != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}
