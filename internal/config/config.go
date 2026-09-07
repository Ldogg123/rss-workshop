package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen, BaseURL, DataDir, Password, PasswordHash, AllowCIDRs string
	ChromiumPath                                                 string
	FlareSolverrURL                                              string
	DatabaseURL                                                  string
	BrowserSlots                                                 int
	FlareSolverrSlots                                            int
	Workers, MaxItems                                            int
	Timeout                                                      time.Duration
	FlareSolverrTimeout                                          time.Duration
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
	if c.PasswordHash == "" && len(c.Password) < 12 {
		return c, fmt.Errorf("set ADMIN_PASSWORD (at least 12 characters) or ADMIN_PASSWORD_HASH")
	}
	for _, v := range []struct {
		name          string
		def, min, max int
		out           *int
	}{{"BROWSER_SLOTS", 2, 1, 8, &c.BrowserSlots}, {"FLARESOLVERR_SLOTS", 1, 1, 4, &c.FlareSolverrSlots}, {"STATIC_WORKERS", 16, 1, 64, &c.Workers}, {"MAX_ITEMS", 500, 1, 10000, &c.MaxItems}} {
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
	c.FlareSolverrTimeout, e = time.ParseDuration(env("FLARESOLVERR_TIMEOUT", "60s"))
	if e != nil || c.FlareSolverrTimeout < 5*time.Second || c.FlareSolverrTimeout > 2*time.Minute {
		return c, fmt.Errorf("FLARESOLVERR_TIMEOUT must be between 5s and 2m")
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
