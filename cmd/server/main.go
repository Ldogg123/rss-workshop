package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
	"rss-workshop/internal/web"
)

// Set by release builds; development builds remain identifiable without Git.
var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

func main() {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobs.Start(ctx)
	app := &web.App{Store: s, Scheduler: jobs, Auth: a, BaseURL: c.BaseURL, Browsers: browsers, Version: version}
	srv := &http.Server{Addr: c.Listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: requestTimeout + 10*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() {
		slog.Info("RSS Workshop listening", "address", c.Listen, "workers", c.Workers, "version", version, "commit", commit)
		done <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case e = <-done:
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	jobs.Wait()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

func openStore(c config.Config) (*store.Store, error) {
	if c.DatabaseURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return store.OpenPostgres(ctx, c.DatabaseURL, c.MaxItems)
	}
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(c.DataDir, "rss.db"), c.MaxItems)
}
