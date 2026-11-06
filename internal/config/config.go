package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"rss-workshop/internal/fetch"
)

type Config struct {
	Listen, BaseURL, DataDir, Password, PasswordHash, AllowCIDRs string
	ChromiumPath                                                 string
	FlareSolverrURL                                              string
	DatabaseURL                                                  string
	BrowserSlots                                                 int
	FlareSolverrSlots                                            int
	Workers, MaxItems                                            int
	LogLevel                                                     slog.Level
	LogFormat                                                    string
	MetricsToken                                                 string
	// FetchUserAgent replaces the static fetcher's User-Agent. Blank keeps
	// fetch.DefaultUserAgent.
	FetchUserAgent string
	// UpgradeBackup saves a verified copy of an SQLite database before its
	// schema is upgraded. Turning it off is the escape hatch when the data
	// directory cannot hold a second copy of the database.
	UpgradeBackup       bool
	Timeout             time.Duration
	FlareSolverrTimeout time.Duration
	// NotifyURL enables health notifications; blank sends none. It and
	// NotifyToken are secrets: a webhook URL often embeds a token, and a topic
	// on a public ntfy server works as a password.
	NotifyURL, NotifyFormat, NotifyToken string
	// NotifyAfterFailures is how many refreshes in a row must fail before a
	// feed counts as failing.
	NotifyAfterFailures int
}

func Load() (Config, error) {
	c := Config{ChromiumPath: os.Getenv("CHROMIUM_PATH"), Listen: env("LISTEN_ADDR", ":8080"), BaseURL: strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:8080"), "/"), DataDir: env("DATA_DIR", "./data"), Password: os.Getenv("ADMIN_PASSWORD"), PasswordHash: os.Getenv("ADMIN_PASSWORD_HASH"), AllowCIDRs: os.Getenv("ALLOW_CIDRS")}
	c.FlareSolverrURL = strings.TrimSpace(os.Getenv("FLARESOLVERR_URL"))
	c.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if c.DatabaseURL != "" {
		database, err := url.Parse(c.DatabaseURL)
		if err != nil || (database.Scheme != "postgres" && database.Scheme != "postgresql") || database.Hostname() == "" || database.Path == "" || database.Path == "/" || database.Fragment != "" {
			// A URL parse error can include its input, including database credentials.
			return c, fmt.Errorf("DATABASE_URL must be a postgres:// or postgresql:// URL with a host and database name")
		}
	}
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return c, fmt.Errorf("PUBLIC_BASE_URL must be an http(s) origin without a path")
	}
	if c.PasswordHash == "" && c.Password == "" {
		return c, fmt.Errorf("set ADMIN_PASSWORD or ADMIN_PASSWORD_HASH")
	}
	for _, v := range []struct {
		name          string
		def, min, max int
		out           *int
	}{{"BROWSER_SLOTS", 2, 1, 8, &c.BrowserSlots}, {"FLARESOLVERR_SLOTS", 1, 1, 4, &c.FlareSolverrSlots}, {"STATIC_WORKERS", 16, 1, 64, &c.Workers}, {"MAX_ITEMS", 500, 1, 10000, &c.MaxItems}, {"NOTIFY_AFTER_FAILURES", 3, 1, 100, &c.NotifyAfterFailures}} {
		n, e := strconv.Atoi(env(v.name, strconv.Itoa(v.def)))
		if e != nil || n < v.min || n > v.max {
			return c, fmt.Errorf("invalid %s", v.name)
		}
		*v.out = n
	}
	c.Timeout, e = time.ParseDuration(env("FETCH_TIMEOUT", "30s"))
	if e != nil || c.Timeout < time.Second || c.Timeout > 2*time.Minute {
		return c, fmt.Errorf("FETCH_TIMEOUT must be between 1s and 2m")
	}
	// A value that would fail every static fetch stops startup instead, where
	// it is visible. A recipe's own User-Agent follows the same rule.
	var ok bool
	if c.FetchUserAgent, ok = fetch.CheckUserAgent(os.Getenv("FETCH_USER_AGENT")); !ok {
		return c, fmt.Errorf("FETCH_USER_AGENT must be at most 256 printable ASCII characters")
	}
	c.FlareSolverrTimeout, e = time.ParseDuration(env("FLARESOLVERR_TIMEOUT", "60s"))
	if e != nil || c.FlareSolverrTimeout < 5*time.Second || c.FlareSolverrTimeout > 2*time.Minute {
		return c, fmt.Errorf("FLARESOLVERR_TIMEOUT must be between 5s and 2m")
	}
	switch strings.ToLower(env("LOG_LEVEL", "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn", "warning":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		return c, fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	c.LogFormat = strings.ToLower(env("LOG_FORMAT", "text"))
	if c.LogFormat != "text" && c.LogFormat != "json" {
		return c, fmt.Errorf("LOG_FORMAT must be text or json")
	}
	// Blank disables the metrics endpoint entirely rather than publishing feed
	// names without a credential. This is a scrape credential, not the admin
	// password: it grants read-only access to counters, never to the library.
	c.MetricsToken = os.Getenv("METRICS_TOKEN")
	if c.MetricsToken != "" && len(c.MetricsToken) < 16 {
		return c, fmt.Errorf("METRICS_TOKEN must be at least 16 characters")
	}
	switch strings.ToLower(strings.TrimSpace(env("UPGRADE_BACKUP", "true"))) {
	case "true":
		c.UpgradeBackup = true
	case "false":
		c.UpgradeBackup = false
	default:
		return c, fmt.Errorf("UPGRADE_BACKUP must be true or false")
	}
	if err := c.loadNotifications(); err != nil {
		return c, err
	}
	return c, nil
}

// loadNotifications reads the health notification settings. None of its errors
// repeats a value: the URL and the token are secrets, and url.Parse's own
// error would quote the URL whole.
func (c *Config) loadNotifications() error {
	c.NotifyURL = strings.TrimSpace(os.Getenv("NOTIFY_URL"))
	c.NotifyToken = strings.TrimSpace(os.Getenv("NOTIFY_TOKEN"))
	c.NotifyFormat = strings.ToLower(strings.TrimSpace(env("NOTIFY_FORMAT", "json")))
	if c.NotifyFormat != "json" && c.NotifyFormat != "ntfy" {
		return fmt.Errorf("NOTIFY_FORMAT must be json or ntfy")
	}
	if c.NotifyURL != "" {
		// A fragment is never sent, so one is a mistake; credentials in the URL
		// would go out as Basic authentication, which NOTIFY_TOKEN replaces.
		u, err := url.Parse(c.NotifyURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(c.NotifyURL, "#") {
			return fmt.Errorf("NOTIFY_URL must be an absolute http:// or https:// URL with a host, and without credentials or a fragment")
		}
	}
	if c.NotifyToken != "" {
		if c.NotifyURL == "" {
			return fmt.Errorf("NOTIFY_TOKEN is set but NOTIFY_URL is blank; set both, or neither")
		}
		// It is sent as a header value, where a line break would add headers of
		// its own and Go would refuse every request at delivery time instead.
		if len(c.NotifyToken) > 4096 || strings.ContainsFunc(c.NotifyToken, func(r rune) bool { return r <= ' ' || r > '~' }) {
			return fmt.Errorf("NOTIFY_TOKEN must be at most 4,096 printable ASCII characters without spaces")
		}
	}
	return nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
