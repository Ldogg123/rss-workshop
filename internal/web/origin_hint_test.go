package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A rejected Origin almost always means the app was opened at an address other
// than PUBLIC_BASE_URL, so the reply names that setting. It never repeats the
// setting's value: the same reply reaches someone who found the server by its
// IP address, and it must not tell them the public hostname.
func TestOriginRejectionNamesTheSettingNotItsValue(t *testing.T) {
	app := &App{BaseURL: "https://feeds.private.example"}
	h := app.Handler()
	for _, tc := range []struct{ name, origin string }{
		{"another address", "http://192.0.2.10:8080"},
		{"no Origin header", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://192.0.2.10:8080/api/login", strings.NewReader(`{"password":"x"}`))
			r.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); w.Code != 403 || err != nil {
				t.Fatal("the request was not rejected with a JSON error", w.Code, err)
			}
			if !strings.Contains(body["error"], "cross-origin request rejected") || !strings.Contains(body["error"], "PUBLIC_BASE_URL") {
				t.Errorf("the rejection does not name the setting to check: %q", body["error"])
			}
			if strings.Contains(w.Body.String(), "feeds.private.example") {
				t.Errorf("the rejection reveals the configured address: %s", w.Body.String())
			}
		})
	}
}
