package config

import (
	"strings"
	"testing"
	"time"
)

func TestAdminPasswordConfig(t *testing.T) {
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL", "FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS", "STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL"} {
		t.Setenv(key, "")
	}
	for _, tc := range []struct {
		name, password string
	}{
		{"short", "x"},
		{"unicode", "雪"},
		{"long", strings.Repeat("long-passphrase-", 20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_PASSWORD", tc.password)
			c, err := Load()
			if err != nil || c.Password != tc.password {
				t.Fatal("configured password was rejected or altered")
			}
		})
	}
	t.Setenv("ADMIN_PASSWORD", "")
	if _, err := Load(); err == nil || err.Error() != "set ADMIN_PASSWORD or ADMIN_PASSWORD_HASH" {
		t.Fatal("missing credentials must require configuration without imposing a length policy")
	}
	t.Setenv("ADMIN_PASSWORD_HASH", "configured-hash-validated-by-auth")
	if _, err := Load(); err != nil {
		t.Fatal("hash-only configuration rejected")
	}
}

func TestFlareSolverrConfig(t *testing.T) {
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL", "FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS", "STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL"} {
		t.Setenv(key, "")
	}
	t.Setenv("ADMIN_PASSWORD", "config-test-password")
	c, err := Load()
	if err != nil || c.FlareSolverrURL != "" || c.FlareSolverrTimeout != time.Minute || c.FlareSolverrSlots != 1 || c.Timeout != 30*time.Second {
		t.Fatal("incorrect optional defaults", err)
	}
	t.Setenv("FLARESOLVERR_URL", " http://10.20.30.40:8191/ ")
	t.Setenv("FLARESOLVERR_TIMEOUT", "90s")
	t.Setenv("FLARESOLVERR_SLOTS", "2")
	c, err = Load()
	if err != nil || c.FlareSolverrURL != "http://10.20.30.40:8191/" || c.FlareSolverrTimeout != 90*time.Second || c.FlareSolverrSlots != 2 || c.Timeout != 30*time.Second {
		t.Fatal("FlareSolverr configuration affected static timeout", err)
	}
	for _, tc := range []struct{ key, value string }{
		{"FLARESOLVERR_TIMEOUT", "4s"}, {"FLARESOLVERR_TIMEOUT", "121s"}, {"FLARESOLVERR_TIMEOUT", "bad"},
		{"FLARESOLVERR_SLOTS", "0"}, {"FLARESOLVERR_SLOTS", "5"}, {"FLARESOLVERR_SLOTS", "bad"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("accepted invalid setting")
			}
		})
	}
}

func TestDatabaseConfig(t *testing.T) {
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL", "FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS", "STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL"} {
		t.Setenv(key, "")
	}
	t.Setenv("ADMIN_PASSWORD", "config-test-password")
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"sqlite_default", "", true},
		{"sqlite_whitespace", "  ", true},
		{"postgres", "postgres://rss:fixture-secret@postgres:5432/rss_workshop?sslmode=verify-full", true},
		{"postgresql", " postgresql://rss:fixture%40secret@db.internal/rss_workshop ", true},
		{"socket_query", "postgres://localhost/rss_workshop?host=/run/postgresql", true},
		{"unsupported", "mysql://rss:fixture-secret@postgres/rss_workshop", false},
		{"missing_host", "postgres:///rss_workshop", false},
		{"missing_database", "postgres://rss:fixture-secret@postgres", false},
		{"empty_database", "postgres://postgres/", false},
		{"fragment", "postgres://rss:fixture-secret@postgres/rss_workshop#fragment", false},
		{"invalid_escape", "postgres://rss:fixture-secret%zz@postgres/rss_workshop", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tc.value)
			c, err := Load()
			if tc.valid {
				if err != nil || c.DatabaseURL != strings.TrimSpace(tc.value) {
					t.Fatal("valid database configuration rejected")
				}
			} else if err == nil || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("invalid database configuration accepted or credential exposed")
			}
		})
	}
}
