package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestAdminPasswordConfig(t *testing.T) {
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL", "FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS", "STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL", "FETCH_USER_AGENT"} {
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
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL", "FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS", "STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL", "FETCH_USER_AGENT"} {
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
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL", "FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS", "STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL", "FETCH_USER_AGENT"} {
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

// Logging and metrics are the two settings an operator reaches for when a
// deployment misbehaves, so an invalid value must fail at startup rather than
// silently falling back to a default they did not choose.
func TestLoggingAndMetricsConfig(t *testing.T) {
	clean := func(t *testing.T) {
		t.Helper()
		for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL",
			"FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS",
			"STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL",
			"LOG_LEVEL", "LOG_FORMAT", "METRICS_TOKEN", "UPGRADE_BACKUP", "FETCH_USER_AGENT"} {
			t.Setenv(key, "")
		}
		t.Setenv("ADMIN_PASSWORD", "long-test-password")
	}

	t.Run("defaults", func(t *testing.T) {
		clean(t)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.LogLevel != slog.LevelInfo || c.LogFormat != "text" {
			t.Errorf("defaults are level=%v format=%q, want info/text", c.LogLevel, c.LogFormat)
		}
		if c.MetricsToken != "" {
			t.Error("metrics are enabled by default")
		}
	})

	for value, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo,
		"warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
		"DEBUG": slog.LevelDebug, "Warn": slog.LevelWarn,
	} {
		t.Run("level "+value, func(t *testing.T) {
			clean(t)
			t.Setenv("LOG_LEVEL", value)
			c, err := Load()
			if err != nil || c.LogLevel != want {
				t.Fatalf("LOG_LEVEL=%q gave %v (%v)", value, c.LogLevel, err)
			}
		})
	}

	for _, bad := range []string{"verbose", "trace", "1", "info ", "critical"} {
		t.Run("rejects level "+bad, func(t *testing.T) {
			clean(t)
			t.Setenv("LOG_LEVEL", bad)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted LOG_LEVEL=%q", bad)
			}
		})
	}

	for _, format := range []string{"text", "json", "JSON"} {
		t.Run("format "+format, func(t *testing.T) {
			clean(t)
			t.Setenv("LOG_FORMAT", format)
			c, err := Load()
			if err != nil || c.LogFormat != strings.ToLower(format) {
				t.Fatalf("LOG_FORMAT=%q gave %q (%v)", format, c.LogFormat, err)
			}
		})
	}
	t.Run("rejects unknown format", func(t *testing.T) {
		clean(t)
		t.Setenv("LOG_FORMAT", "logfmt")
		if _, err := Load(); err == nil {
			t.Fatal("accepted an unknown LOG_FORMAT")
		}
	})

	t.Run("upgrade backup", func(t *testing.T) {
		for value, want := range map[string]bool{"": true, "true": true, "TRUE": true, "false": false, " False ": false} {
			clean(t)
			t.Setenv("UPGRADE_BACKUP", value)
			c, err := Load()
			if err != nil || c.UpgradeBackup != want {
				t.Fatalf("UPGRADE_BACKUP=%q gave %v (%v)", value, c.UpgradeBackup, err)
			}
		}
		for _, value := range []string{"no", "0", "off"} {
			clean(t)
			t.Setenv("UPGRADE_BACKUP", value)
			if _, err := Load(); err == nil {
				t.Fatalf("accepted UPGRADE_BACKUP=%q", value)
			}
		}
	})

	t.Run("metrics token", func(t *testing.T) {
		clean(t)
		t.Setenv("METRICS_TOKEN", strings.Repeat("k", 32))
		c, err := Load()
		if err != nil || c.MetricsToken != strings.Repeat("k", 32) {
			t.Fatalf("token not accepted: %v", err)
		}
	})
	t.Run("rejects a guessable metrics token", func(t *testing.T) {
		clean(t)
		t.Setenv("METRICS_TOKEN", "short")
		if _, err := Load(); err == nil {
			t.Fatal("accepted a 5-character metrics token")
		}
	})
}

// FETCH_USER_AGENT goes into a request header as it is. A line break would let
// it add headers of its own, and Go refuses one at request time, failing every
// static fetch, so an unusable value stops startup instead. The error does not
// repeat the value.
func TestFetchUserAgentConfig(t *testing.T) {
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL", "FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS", "STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL", "FETCH_USER_AGENT"} {
		t.Setenv(key, "")
	}
	t.Setenv("ADMIN_PASSWORD", "config-test-password")
	for value, want := range map[string]string{
		"":    "",
		"   ": "",
		" GoRSS/0.1 (+self-hosted feed builder)\n": "GoRSS/0.1 (+self-hosted feed builder)",
		"Reader/2 (+mailto:ops@example.net; ~x)":   "Reader/2 (+mailto:ops@example.net; ~x)",
		strings.Repeat("a", 256):                   strings.Repeat("a", 256),
	} {
		t.Setenv("FETCH_USER_AGENT", value)
		c, err := Load()
		if err != nil || c.FetchUserAgent != want {
			t.Errorf("FETCH_USER_AGENT=%q gave %q (%v), want %q", value, c.FetchUserAgent, err, want)
		}
	}
	for _, value := range []string{
		"secret-agent\r\nX-Injected: 1", "secret-agent\nX-Injected: 1", "secret\tagent",
		"secret-agent\x1b[0m", "secret-agent\x7f", "secret-agenté", "secret-agent\xff",
		strings.Repeat("a", 257),
	} {
		t.Setenv("FETCH_USER_AGENT", value)
		if _, err := Load(); err == nil {
			t.Errorf("accepted FETCH_USER_AGENT=%q", value)
		} else if strings.Contains(err.Error(), "secret") {
			t.Errorf("the error repeats the value: %v", err)
		}
	}
}

// NOTIFY_URL and NOTIFY_TOKEN are secrets: webhook URLs carry tokens in their
// path or query, and a topic on a public ntfy server works as a password. A
// startup error must say what is wrong without repeating either, because it
// is the line Compose shows in docker compose logs.
func TestNotificationConfig(t *testing.T) {
	clean := func(t *testing.T) {
		t.Helper()
		for _, key := range []string{"ADMIN_PASSWORD_HASH", "CHROMIUM_PATH", "FLARESOLVERR_URL",
			"FLARESOLVERR_TIMEOUT", "FLARESOLVERR_SLOTS", "FETCH_TIMEOUT", "BROWSER_SLOTS",
			"STATIC_WORKERS", "MAX_ITEMS", "PUBLIC_BASE_URL", "DATABASE_URL", "LOG_LEVEL", "LOG_FORMAT",
			"METRICS_TOKEN", "UPGRADE_BACKUP", "FETCH_USER_AGENT",
			"NOTIFY_URL", "NOTIFY_FORMAT", "NOTIFY_TOKEN", "NOTIFY_AFTER_FAILURES"} {
			t.Setenv(key, "")
		}
		t.Setenv("ADMIN_PASSWORD", "long-test-password")
	}

	t.Run("off by default", func(t *testing.T) {
		clean(t)
		c, err := Load()
		if err != nil || c.NotifyURL != "" || c.NotifyToken != "" || c.NotifyFormat != "json" || c.NotifyAfterFailures != 3 {
			t.Fatalf("defaults: url=%q token=%q format=%q after=%d, %v", c.NotifyURL, c.NotifyToken, c.NotifyFormat, c.NotifyAfterFailures, err)
		}
	})

	t.Run("valid", func(t *testing.T) {
		for _, tc := range []struct{ url, format, token, after, wantFormat string }{
			// A private address is allowed: the endpoint is trusted configuration.
			{" http://192.168.1.20:8090/rss-alerts ", "ntfy", " tk_fixturetoken0123456789 ", "1", "ntfy"},
			{"https://hooks.example/notify?token=fixture-secret", "JSON", "", "100", "json"},
			{"https://ntfy.example/topic", " Ntfy ", "", "", "ntfy"},
		} {
			clean(t)
			t.Setenv("NOTIFY_URL", tc.url)
			t.Setenv("NOTIFY_FORMAT", tc.format)
			t.Setenv("NOTIFY_TOKEN", tc.token)
			t.Setenv("NOTIFY_AFTER_FAILURES", tc.after)
			c, err := Load()
			if err != nil {
				t.Fatalf("%s rejected: %v", tc.url, err)
			}
			if c.NotifyURL != strings.TrimSpace(tc.url) || c.NotifyToken != strings.TrimSpace(tc.token) || c.NotifyFormat != tc.wantFormat {
				t.Errorf("read url=%q token=%q format=%q", c.NotifyURL, c.NotifyToken, c.NotifyFormat)
			}
		}
	})

	const secret = "fixture-secret-value"
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"credentials", map[string]string{"NOTIFY_URL": "https://user:" + secret + "@hooks.example/notify"}, "NOTIFY_URL"},
		{"fragment", map[string]string{"NOTIFY_URL": "https://hooks.example/" + secret + "#section"}, "NOTIFY_URL"},
		{"empty fragment", map[string]string{"NOTIFY_URL": "https://hooks.example/" + secret + "#"}, "NOTIFY_URL"},
		{"scheme", map[string]string{"NOTIFY_URL": "ftp://hooks.example/" + secret}, "NOTIFY_URL"},
		{"opaque", map[string]string{"NOTIFY_URL": "mailto:" + secret + "@example.com"}, "NOTIFY_URL"},
		{"relative", map[string]string{"NOTIFY_URL": "/notify/" + secret}, "NOTIFY_URL"},
		{"no host", map[string]string{"NOTIFY_URL": "http:///" + secret}, "NOTIFY_URL"},
		{"unparseable", map[string]string{"NOTIFY_URL": "http://hooks.example/%zz" + secret}, "NOTIFY_URL"},
		{"line break", map[string]string{"NOTIFY_URL": "http://hooks.example/" + secret + "\nX: y"}, "NOTIFY_URL"},
		{"token without URL", map[string]string{"NOTIFY_TOKEN": secret}, "NOTIFY_TOKEN"},
		{"token line break", map[string]string{"NOTIFY_URL": "https://ntfy.example/topic", "NOTIFY_TOKEN": secret + "\r\nX-Injected: 1"}, "NOTIFY_TOKEN"},
		{"token space", map[string]string{"NOTIFY_URL": "https://ntfy.example/topic", "NOTIFY_TOKEN": secret + " more"}, "NOTIFY_TOKEN"},
		{"token non-ASCII", map[string]string{"NOTIFY_URL": "https://ntfy.example/topic", "NOTIFY_TOKEN": secret + "é"}, "NOTIFY_TOKEN"},
		{"token too long", map[string]string{"NOTIFY_URL": "https://ntfy.example/topic", "NOTIFY_TOKEN": secret + strings.Repeat("x", 4096)}, "NOTIFY_TOKEN"},
		{"format", map[string]string{"NOTIFY_FORMAT": "slack"}, "NOTIFY_FORMAT"},
		{"threshold zero", map[string]string{"NOTIFY_AFTER_FAILURES": "0"}, "NOTIFY_AFTER_FAILURES"},
		{"threshold too high", map[string]string{"NOTIFY_AFTER_FAILURES": "101"}, "NOTIFY_AFTER_FAILURES"},
		{"threshold not a number", map[string]string{"NOTIFY_AFTER_FAILURES": "three"}, "NOTIFY_AFTER_FAILURES"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clean(t)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			_, err := Load()
			if err == nil {
				t.Fatal("accepted an invalid notification setting")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name %s: %v", tc.want, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "hooks.example") {
				t.Errorf("error repeats the secret value: %v", err)
			}
		})
	}
}
