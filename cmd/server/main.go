package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/browser"
	"rss-workshop/internal/config"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/flaresolverr"
	"rss-workshop/internal/notify"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
	"rss-workshop/internal/web"
)

// Set by release builds; development builds remain identifiable without Git.
var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

func main() {
	// Configuration has not been parsed yet, so read the format directly: a
	// startup failure is exactly the line an operator needs, and it should reach
	// a log collector in the same shape as every other line. Anything but "json"
	// falls back to text here, and config.Load reports an invalid value.
	setupLogging(config.Config{LogLevel: slog.LevelInfo, LogFormat: strings.ToLower(os.Getenv("LOG_FORMAT"))})
	if e := run(); e != nil {
		slog.Error("server stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	flags := flag.NewFlagSet("rss-workshop", flag.ContinueOnError)
	showVersion := flags.Bool("version", false, "print the build version and exit")
	healthcheck := flags.Bool("healthcheck", false, "check readiness of the local server and exit")
	if err := flags.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || (*showVersion && *healthcheck) {
		return errors.New("use -version, -healthcheck, or no arguments to start the server")
	}
	if *showVersion {
		fmt.Printf("RSS Workshop %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	}
	if *healthcheck {
		addr := os.Getenv("LISTEN_ADDR")
		if addr == "" {
			addr = ":8080"
		}
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return e
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		c := http.Client{Timeout: 2 * time.Second}
		r, e := c.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
		if e != nil {
			return e
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("readiness status: %d", r.StatusCode)
		}
		return nil
	}

	c, e := config.Load()
	if e != nil {
		return e
	}
	setupLogging(c)
	a, e := auth.New(c.Password, c.PasswordHash, strings.HasPrefix(c.BaseURL, "https://"))
	if e != nil {
		return e
	}
	c.Password = ""
	c.PasswordHash = ""
	s, e := openStore(c)
	if e != nil {
		return e
	}
	defer s.DB.Close()
	f, e := fetch.New(c.Timeout, c.AllowCIDRs)
	if e != nil {
		return e
	}
	f.UserAgent = c.FetchUserAgent
	jobs := scheduler.New(s, f, c.Workers, c.Timeout)
	requestTimeout := c.Timeout
	if c.FlareSolverrURL != "" {
		solver, err := flaresolverr.New(c.FlareSolverrURL, c.FlareSolverrSlots, c.FlareSolverrTimeout, f)
		if err != nil {
			return err
		}
		defer solver.Close()
		jobs.FlareSolverr = solver
		jobs.FlareSolverrTimeout = c.FlareSolverrTimeout + 10*time.Second
		requestTimeout = max(requestTimeout, jobs.FlareSolverrTimeout)
	}
	var browsers *browser.Pool
	if c.ChromiumPath != "" {
		browsers, e = browser.New(c.ChromiumPath, c.BrowserSlots, c.Timeout, f)
		if e != nil {
			return e
		}
		defer browsers.Close()
		jobs.Renderer = browsers
	}
	var notifier *notify.Notifier
	if c.NotifyURL != "" {
		notifier, e = notify.New(c.NotifyURL, c.NotifyFormat, c.NotifyToken, c.BaseURL)
		if e != nil {
			return e
		}
		notifier.Start()
		// Closed explicitly after the scheduler stops, so the events of the
		// last refreshes are sent; this covers an early return.
		defer notifier.Close()
		jobs.Notifier = notifier
		jobs.NotifyAfter = c.NotifyAfterFailures
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobs.Start(ctx)
	app := &web.App{Store: s, Scheduler: jobs, Auth: a, BaseURL: c.BaseURL, Browsers: browsers, Version: version, MetricsToken: c.MetricsToken, Notifier: notifier}
	srv := &http.Server{Addr: c.Listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: requestTimeout + 10*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() {
		slog.Info("RSS Workshop listening",
			"address", c.Listen, "base_url", c.BaseURL,
			"version", version, "commit", commit, "built", buildDate,
			"database", databaseKind(c), "workers", c.Workers, "max_items", c.MaxItems,
			"browser", browsers != nil, "flaresolverr", jobs.FlareSolverr != nil,
			"metrics", c.MetricsToken != "", "custom_user_agent", c.FetchUserAgent != "",
			"notifications", notifier.Kind(), "log_level", c.LogLevel.String())
		done <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case e = <-done:
		stop()
	}
	slog.Info("shutting down", "grace", "10s")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	jobs.Wait()
	// The last refreshes may have queued events; this sends them within a
	// bound of its own rather than waiting out the coalescing window.
	notifier.Close()
	slog.Info("stopped")
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

// setupLogging replaces the default logger before any component starts, so the
// chosen level and format apply to every line an operator sees in docker logs.
func setupLogging(c config.Config) {
	options := &slog.HandlerOptions{Level: c.LogLevel}
	var handler slog.Handler = slog.NewTextHandler(os.Stderr, options)
	if c.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, options)
	}
	slog.SetDefault(slog.New(handler))
}

// databaseKind names the backend without revealing the connection string, which
// carries credentials.
func databaseKind(c config.Config) string {
	if c.DatabaseURL != "" {
		return "postgresql"
	}
	return "sqlite"
}

func openStore(c config.Config) (*store.Store, error) {
	if c.DatabaseURL != "" {
		// OpenPostgres bounds connecting and any schema upgrade itself. An
		// upgrade can take minutes, so a stop signal cancels it, and PostgreSQL
		// rolls it back at once, instead of ending the process while the server
		// goes on running the upgrade with its locks held.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		s, err := store.OpenPostgres(ctx, c.DatabaseURL, c.MaxItems)
		if err == nil && ctx.Err() != nil {
			// The signal arrived too late to cancel anything, but it was caught
			// here, so stop rather than start serving.
			s.DB.Close()
			return nil, errors.New("startup was interrupted")
		}
		return s, err
	}
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return nil, err
	}
	if err := checkDataDir(c.DataDir); err != nil {
		return nil, err
	}
	s, err := store.OpenWithOptions(filepath.Join(c.DataDir, "rss.db"), c.MaxItems, store.OpenOptions{SkipUpgradeBackup: !c.UpgradeBackup})
	if errors.Is(err, store.ErrUpgradeBackup) {
		return nil, fmt.Errorf("%w; fix the cause and start again, or stop the app, take your own backup, and set UPGRADE_BACKUP=false for one start", err)
	}
	return s, err
}

// checkDataDir refuses a data directory this process may not write to before
// SQLite opens it. SQLite reports that as "unable to open database file (14)",
// or "attempt to write a readonly database (1544)" when a database is already
// there, and neither names the directory, the user or the fix. This is what a
// first Docker start meets when the host directory still belongs to root.
//
// The probe is a temporary file, created and removed, because creating files
// there is what SQLite needs for its journal; a refused directory is left as
// it was found. Only a permission or read-only error is refused. Any other
// failure, a full disk for one, is left for the database to report as before.
func checkDataDir(directory string) error {
	probe, err := os.CreateTemp(directory, ".write-check-*")
	if err == nil {
		probe.Close()
		err = os.Remove(probe.Name())
	}
	readOnly := errors.Is(err, syscall.EROFS)
	// The cause alone is reported: the full error adds the probe's random name.
	var cause syscall.Errno
	if !(readOnly || errors.Is(err, fs.ErrPermission)) || !errors.As(err, &cause) {
		return nil
	}
	hint := "the directory must be writable by that user; with Docker Compose, run sudo chown 65532:65532 <your RSS_DATA_DIR> on the host"
	if readOnly {
		hint = "mount the directory read-write"
	}
	if absolute, e := filepath.Abs(directory); e == nil {
		directory = absolute
	}
	return fmt.Errorf("cannot write to the data directory %s (DATA_DIR) as uid %d, gid %d: %w; %s, then start again; see docs/deployment.md",
		directory, os.Geteuid(), os.Getegid(), cause, hint)
}
