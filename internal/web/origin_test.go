package web

import (
	"net/http/httptest"
	"testing"
)

func TestForwardedOrigin(t *testing.T) {
	app := &App{BaseURL: "http://localhost:8080"}
	for _, tc := range []struct {
		name, origin, site string
		want               bool
	}{
		{"canonical", "http://localhost:8080", "", true},
		{"forwarded browser", "http://localhost:18081", "same-origin", true},
		{"changed forwarded port", "http://localhost:18082", "same-origin", true},
		{"unverified port", "http://localhost:18082", "", false},
		{"other local app", "http://localhost:18082", "same-site", false},
		{"cross-site", "http://localhost:18082", "cross-site", false},
		{"other hostname", "http://attacker.example:18082", "same-origin", false},
		{"hostname suffix", "http://localhost.attacker.example:18082", "same-origin", false},
		{"changed scheme", "https://localhost:18082", "same-origin", false},
		{"opaque origin", "null", "same-origin", false},
		{"missing origin", "", "same-origin", false},
		{"credentials", "http://user@localhost:18082", "same-origin", false},
		{"invalid port", "http://localhost:bad", "same-origin", false},
		{"path", "http://localhost:18082/path", "same-origin", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://localhost:8080/api/login", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("X-Forwarded-Host", "localhost:18082")
			if got := app.acceptsOrigin(r); got != tc.want {
				t.Fatalf("accepted=%v, want %v", got, tc.want)
			}
		})
	}
}
