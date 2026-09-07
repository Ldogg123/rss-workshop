package store

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"rss-workshop/internal/model"
	"rss-workshop/internal/testutil"
)

func TestPostgresRejectsUnsupportedEncodingBeforeSQL(t *testing.T) {
	for _, tc := range []struct{ server, client string }{{"LATIN1", "UTF8"}, {"SQL_ASCII", "UTF8"}, {"UTF8", "LATIN1"}} {
		t.Run(tc.server+"/"+tc.client, func(t *testing.T) {
			// A protocol fixture exercises the real pgx handshake and error
			// wrapping without requiring CREATEDB rights or a legacy database.
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			t.Cleanup(func() {
				listener.Close()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				backend := pgproto3.NewBackend(conn, conn)
				message, err := backend.ReceiveStartupMessage()
				if err != nil {
					done <- err
					return
				}
				startup, ok := message.(*pgproto3.StartupMessage)
				if !ok || startup.Parameters["client_encoding"] != "UTF8" {
					done <- errors.New("connection did not request UTF8 despite the conflicting URL setting")
					return
				}
				backend.Send(&pgproto3.AuthenticationOk{})
				backend.Send(&pgproto3.ParameterStatus{Name: "server_encoding", Value: tc.server})
				backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: tc.client})
				backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if err := backend.Flush(); err != nil {
					done <- err
					return
				}
				// Rejection must precede even a ping or schema query.
				message, err = backend.Receive()
				if err == nil {
					if _, closing := message.(*pgproto3.Terminate); !closing {
						done <- fmt.Errorf("unsupported encoding received SQL/protocol message %T", message)
						return
					}
				}
				done <- nil
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			raw := "postgres://fixture:private-fixture-password@" + listener.Addr().String() + "/rss?sslmode=disable&client_encoding=LATIN1"
			s, err := OpenPostgres(ctx, raw, 10)
			if s != nil || !errors.Is(err, errPostgresEncoding) {
				t.Fatal("unsupported encoding was accepted or lacked a clear startup error")
			}
			if strings.Contains(err.Error(), "private-fixture-password") || strings.Contains(err.Error(), listener.Addr().String()) {
				t.Fatal("encoding error exposed connection settings")
			}
		})
	}
}

func TestPostgresOverridesConflictingClientEncoding(t *testing.T) {
	raw := testutil.PostgresURL(t)
	if raw == "" {
		t.Skip("set RSS_TEST_POSTGRES_URL to test PostgreSQL client encoding")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("invalid isolated PostgreSQL fixture URL")
	}
	query := u.Query()
	query.Set("client_encoding", "LATIN1")
	u.RawQuery = query.Encode()
	s, err := OpenPostgres(t.Context(), u.String(), 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	for _, reconnect := range []bool{false, true} {
		if reconnect {
			// Close idle connections so the next query requires a fresh
			// handshake, rather than only checking the startup connection.
			s.DB.SetMaxIdleConns(0)
		}
		var encoding string
		if err := s.DB.QueryRowContext(t.Context(), "SHOW client_encoding").Scan(&encoding); err != nil || encoding != "UTF8" {
			t.Fatal("client encoding was not enforced for every connection", err)
		}
		f := model.Feed{Title: "Café 日本語 😀", URL: "https://example.com", Interval: 60}
		id, err := s.Save(t.Context(), f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Get(t.Context(), id)
		if err != nil || got.Title != f.Title {
			t.Fatal("connection setting corrupted Unicode text", err)
		}
	}
}
