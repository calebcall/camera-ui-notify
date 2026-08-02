package backend

import (
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

func TestDeepLinkLabel(t *testing.T) {
	tests := []struct {
		name  string
		notif sdk.Notification
		want  string
	}{
		{
			name:  "relative detection deep link",
			notif: sdk.Notification{DeepLink: "/cameras/cam-1?startTs=123"},
			want:  DeepLinkLabelCamera,
		},
		{
			name:  "absolute detection deep link",
			notif: sdk.Notification{DeepLink: "https://camera.example.com/cameras/cam-1"},
			want:  DeepLinkLabelCamera,
		},
		{
			name:  "absolute detection deep link under a sub-path mount",
			notif: sdk.Notification{DeepLink: "https://example.com/camera-ui/cameras/cam-1"},
			want:  DeepLinkLabelCamera,
		},
		{
			name:  "camera list page is not a single camera",
			notif: sdk.Notification{DeepLink: "https://camera.example.com/cameras"},
			want:  DeepLinkLabelOther,
		},
		{
			name:  "plugin update deep link",
			notif: sdk.Notification{DeepLink: "https://camera.example.com/settings/plugins"},
			want:  DeepLinkLabelOther,
		},
		{
			name:  "relative system deep link",
			notif: sdk.Notification{DeepLink: "/system"},
			want:  DeepLinkLabelOther,
		},
		{
			name: "cameraId with an uninformative deep link",
			notif: sdk.Notification{
				DeepLink: "https://camera.example.com/",
				Data:     map[string]string{"cameraId": "cam-1"},
			},
			want: DeepLinkLabelCamera,
		},
		{
			name: "cameraId does not override a non-camera destination",
			notif: sdk.Notification{
				DeepLink: "https://camera.example.com/settings/plugins",
				Data:     map[string]string{"cameraId": "cam-1"},
			},
			want: DeepLinkLabelOther,
		},
		{
			name:  "no deep link and no cameraId",
			notif: sdk.Notification{},
			want:  DeepLinkLabelOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeepLinkLabel(tc.notif); got != tc.want {
				t.Errorf("DeepLinkLabel(%+v) = %q, want %q", tc.notif, got, tc.want)
			}
		})
	}
}

func TestDeepLinkCameraSegment(t *testing.T) {
	cases := []struct {
		name     string
		deepLink string
		want     string
	}{
		{"absolute camera link", "https://cam.example/cameras/Patio?startTs=123", "Patio"},
		{"router-relative camera link", "/cameras/Patio", "Patio"},
		{"sub-path mount", "https://cam.example/camera-ui/cameras/Patio", "Patio"},
		{"percent-encoded name is decoded", "https://cam.example/cameras/Front%20Door", "Front Door"},
		{"camera list page has no segment", "/cameras", ""},
		{"trailing slash has no segment", "/cameras/", ""},
		{"non-camera route", "/settings/notifications", ""},
		{"empty deep link", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeepLinkCameraSegment(sdk.Notification{DeepLink: tc.deepLink})
			if got != tc.want {
				t.Errorf("DeepLinkCameraSegment(%q) = %q, want %q", tc.deepLink, got, tc.want)
			}
		})
	}
}
