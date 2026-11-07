package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"rss-workshop/internal/config"
	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

func TestDefaultStoreCreatesSQLite(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "data")
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500})
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err := os.Stat(filepath.Join(directory, "rss.db")); err != nil {
		t.Fatal("SQLite database was not created", err)
	}
}

func TestPostgresFailureDoesNotCreateSQLite(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "must-not-exist")
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500,
		DatabaseURL: "postgres://rss:private-fixture-password%zz@localhost/rss_workshop"})
	if s != nil || err == nil {
		t.Fatal("invalid PostgreSQL configuration silently opened a store")
	}
	if strings.Contains(err.Error(), "private-fixture-password") {
		t.Fatal("database error exposed the credential")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("PostgreSQL failure touched SQLite's data directory")
	}
}

func TestPostgresDoesNotRequireDataDirectory(t *testing.T) {
	databaseURL := testutil.PostgresURL(t)
	if databaseURL == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to a disposable database")
	}
	// A file cannot be used as a data directory. PostgreSQL must not touch it.
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("keep this file"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(config.Config{DataDir: path, MaxItems: 500, DatabaseURL: databaseURL})
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	if _, err := s.Save(context.Background(), model.Feed{Title: "PostgreSQL startup", URL: "https://example.com", Interval: 60}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep this file" {
		t.Fatal("PostgreSQL startup changed the data directory path")
	}
}

// Startup copies an older SQLite database before upgrading it, and a failed
// copy names both ways forward instead of upgrading anyway.
func TestStartupUpgradeCopyFollowsConfiguration(t *testing.T) {
	legacy := func(t *testing.T) string {
		t.Helper()
		directory := filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		schema, err := os.ReadFile("../../internal/store/testdata/schema_v4.sql")
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(directory, "rss.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatal(err)
		}
		return directory
	}
	directory := legacy(t)
	s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true})
	if err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	if copies, err := filepath.Glob(filepath.Join(directory, "backups", "pre-upgrade-schema-4-*", "manifest.json")); err != nil || len(copies) != 1 {
		t.Fatal("startup did not copy the database before upgrading it", copies, err)
	}

	directory = legacy(t)
	if err := os.WriteFile(filepath.Join(directory, "backups"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: true}); s != nil || err == nil || !strings.Contains(err.Error(), "UPGRADE_BACKUP=false") {
		t.Fatal("a failed copy did not explain how to continue", err)
	}
	s, err = openStore(config.Config{DataDir: directory, MaxItems: 500, UpgradeBackup: false})
	if err != nil {
		t.Fatal("UPGRADE_BACKUP=false did not upgrade without a copy", err)
	}
	s.DB.Close()
}

// A stop signal during a PostgreSQL schema upgrade, such as the SIGTERM of a
// container restart, rolls the upgrade back on the server before the process
// exits. Otherwise PostgreSQL went on running it with its locks held, for up
// to the upgrade's time limit, and every start until then failed at the lock.
//
// pgx also asks the server to cancel, but from a goroutine that the exiting
// process does not wait for, so its request arrives in some runs and not in
// others. The child therefore connects through a proxy that drops every
// CancelRequest: only the app's own pg_cancel_backend, which is an ordinary
// query, reaches the server, and removing it fails this test every time.
func TestStopSignalRollsBackPostgresUpgrade(t *testing.T) {
	if raw := os.Getenv("RSS_TEST_UPGRADE_CHILD_URL"); raw != "" {
		// The child starts as the server does and exits as main does.
		_, err := openStore(config.Config{DatabaseURL: raw, MaxItems: 500})
		fmt.Println(err)
		os.Exit(1)
	}
	raw := testutil.PostgresURL(t)
	if raw == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to a disposable database")
	}
	db, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal("cannot configure the PostgreSQL fixture")
	}
	// Registered after PostgresURL, so this runs before the schema is dropped.
	t.Cleanup(func() {
		defer db.Close()
		// A failed run leaves the upgrade sleeping on the server with the
		// database-wide schema lock, which other packages' tests need.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = db.ExecContext(ctx, "SELECT pg_terminate_backend(pid) FROM (SELECT DISTINCT pid FROM pg_locks WHERE relation='feeds'::regclass AND pid<>pg_backend_pid()) l")
	})
	schema, err := os.ReadFile("../../internal/store/testdata/postgres_schema_v5.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The upgrade sleeps far longer than this test waits for its locks to go.
	const sleep = 30 * time.Second
	if _, err := db.ExecContext(t.Context(), string(schema)+`;
INSERT INTO feeds(id,rss_token,title,url,recipe,interval,enabled,next_run) VALUES('kept','kept-token','Kept','https://example.com','{}',600,true,0);
CREATE FUNCTION slow_upgrade() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(`+strconv.Itoa(int(sleep/time.Second))+`); RETURN NEW; END $$;
CREATE TRIGGER slow_upgrade BEFORE UPDATE ON schema_version FOR EACH ROW EXECUTE FUNCTION slow_upgrade()`); err != nil {
		t.Fatal(err)
	}
	// Only this test's upgrade locks this schema's feeds table.
	locked := func(query string) bool {
		t.Helper()
		var found bool
		if err := db.QueryRowContext(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE l.relation='feeds'::regclass`+query+`)`).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	childURL := throughCancelFilter(t, db, raw)
	child := exec.Command(os.Args[0], "-test.run=^TestStopSignalRollsBackPostgresUpgrade$")
	child.Env = append(os.Environ(), "RSS_TEST_UPGRADE_CHILD_URL="+childURL)
	var out bytes.Buffer
	child.Stdout, child.Stderr = &out, &out
	started := time.Now()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); !locked(" AND a.wait_event='PgSleep'"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			child.Process.Kill()
			child.Wait()
			t.Fatal("the upgrade did not start:\n" + out.String())
		}
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	want := "cannot migrate PostgreSQL database from schema version 5 to 6: startup was interrupted; the database is unchanged"
	if child.ProcessState.ExitCode() != 1 || !strings.Contains(out.String(), want) {
		t.Fatalf("a stop signal during the upgrade did not stop startup with %q:\n%s", want, out.String())
	}
	for deadline := time.Now().Add(3 * time.Second); locked(""); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("PostgreSQL went on running the upgrade after the process exited")
		}
	}
	// The sleep began after the child started, so locks found gone before
	// started+sleep were not released by the sleep ending on its own.
	if time.Since(started) >= sleep {
		t.Fatal("the run took longer than the upgrade's sleep, so it cannot tell whether the upgrade was canceled")
	}
	var version int
	var title string
	if err := db.QueryRowContext(t.Context(), "SELECT version,(SELECT title FROM feeds WHERE id='kept') FROM schema_version").Scan(&version, &title); err != nil || version != 5 || title != "Kept" {
		t.Fatal("the interrupted upgrade changed the database", version, title, err)
	}
}

// throughCancelFilter returns raw with its server replaced by a local proxy
// that forwards every connection except a CancelRequest, which it closes
// unread. Any other connection, including one that runs pg_cancel_backend,
// opens with an SSLRequest or a StartupMessage instead. pgx encrypts a cancel
// connection when the main one is encrypted, so the URL turns TLS off for the
// proxy to read it. It also turns off the server's check for a dropped client
// where the server has one, since that would end the upgrade without a
// cancellation too.
func throughCancelFilter(t *testing.T, db *sql.DB, raw string) string {
	t.Helper()
	settings, err := pgconn.ParseConfig(raw)
	if err != nil {
		t.Fatal("cannot read the PostgreSQL fixture's address")
	}
	network, address := pgconn.NetworkAddress(settings.Host, settings.Port)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				// Every message that opens a connection starts with its length
				// and a code; 80877102 is a CancelRequest.
				var head [8]byte
				client.SetReadDeadline(time.Now().Add(10 * time.Second))
				if _, err := io.ReadFull(client, head[:]); err != nil || binary.BigEndian.Uint32(head[4:]) == 80877102 {
					return
				}
				client.SetReadDeadline(time.Time{})
				server, err := net.DialTimeout(network, address, 5*time.Second)
				if err != nil {
					return
				}
				defer server.Close()
				if _, err := server.Write(head[:]); err != nil {
					return
				}
				// Either side closing closes both, as a direct connection would.
				done := make(chan struct{}, 2)
				go func() { io.Copy(server, client); done <- struct{}{} }()
				go func() { io.Copy(client, server); done <- struct{}{} }()
				<-done
				client.Close()
				server.Close()
				<-done
			}()
		}
	}()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("cannot read the PostgreSQL fixture's URL")
	}
	u.Host = listener.Addr().String()
	query := u.Query()
	query.Del("host")
	query.Del("port")
	query.Del("sslnegotiation")
	query.Set("sslmode", "disable")
	// client_connection_check_interval exists from PostgreSQL 14.
	var check sql.NullString
	if err := db.QueryRowContext(t.Context(), "SELECT current_setting('client_connection_check_interval',true)").Scan(&check); err != nil {
		t.Fatal(err)
	}
	if check.Valid {
		query.Set("client_connection_check_interval", "0")
	}
	u.RawQuery = query.Encode()
	return u.String()
}
