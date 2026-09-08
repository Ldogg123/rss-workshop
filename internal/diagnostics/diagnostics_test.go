package diagnostics

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rss-workshop/internal/model"
)

func TestSafeText(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"Match 2: expected a date; received \"yesterday-ish\"", "Match 2: expected a date; received \"yesterday-ish\""},
		{"loading https://user:secret@example.com/private?token=hidden failed", "loading [URL omitted] failed"},
		{"connect postgres://user:secret@database/library", "connect [URL omitted]"},
		{"Authorization: Bearer private\nCookie: session=private\nSet-Cookie: reader=private", "Authorization: [redacted]\nCookie: [redacted]\nSet-Cookie: [redacted]"},
		{"password=secret api_key='private' access-token=private", "password=[redacted] api_key=[redacted] access-token=[redacted]"},
		{"bad\x00value\xff", "bad�value�"},
	} {
		if got := SafeText(tc.input, 1000); got != tc.want {
			t.Errorf("SafeText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	for limit := 0; limit < 10; limit++ {
		got := SafeText("日付の文字列", limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatalf("limit %d: invalid bounded text %q", limit, got)
		}
	}
}

func TestSanitizeBoundsAndDetachedCopy(t *testing.T) {
	matches, items := 25, 0
	warnings := make([]string, MaxWarnings+5)
	for i := range warnings {
		warnings[i] = strings.Repeat("日", 500)
	}
	in := &model.RunDiagnostics{Version: 1, RecipeVersion: 3, RequestedMode: "auto", SelectorType: "xpath", Started: time.Now(), DurationMS: 1500,
		Attempts: []model.FetchAttempt{{Mode: "static", Outcome: "failed", Stage: "extract", Status: 200, Matches: &matches, Items: &items, Error: "loading https://example.com/private?key=hidden", Warnings: warnings}}}
	out := Sanitize(in)
	if out == in || len(out.Attempts) != 1 || out.RecipeVersion != 3 || out.DurationMS != 1500 || out.Started.Location() != time.UTC {
		t.Fatalf("trace changed or aliased: %+v", out)
	}
	a := out.Attempts[0]
	if len(a.Warnings) != MaxWarnings || a.WarningsOmitted != 5 || *a.Matches != 25 || *a.Items != 0 || a.Error != "loading [URL omitted]" {
		t.Fatalf("attempt bounds: %+v", a)
	}
	for _, warning := range a.Warnings {
		if len(warning) > 512 || !utf8.ValidString(warning) {
			t.Fatal("warning limit split UTF-8")
		}
	}
	warnings[0] = "changed"
	matches = 999
	in.Attempts[0].Mode = "browser"
	if a.Warnings[0] == "changed" || *a.Matches != 25 || a.Mode != "static" {
		t.Fatal("input mutation changed the retained trace")
	}
	if Sanitize(nil) != nil {
		t.Fatal("legacy nil trace acquired invented details")
	}
}

func TestSanitizeMalformedMetadata(t *testing.T) {
	negative, huge := -1, 10000000
	in := &model.RunDiagnostics{RecipeVersion: -1, RequestedMode: "https://private.example", SelectorType: "bad", Started: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), DurationMS: -1,
		Attempts: []model.FetchAttempt{{Mode: "bad", Outcome: "bad", Stage: "bad", Status: 999, DurationMS: 999999999, Bytes: -1, Matches: &negative, Items: &huge}, {}, {}}}
	out := Sanitize(in)
	a := out.Attempts[0]
	if out.Version != 1 || out.RecipeVersion != 0 || out.RequestedMode != "unknown" || out.SelectorType != "unknown" || !out.Started.IsZero() || out.DurationMS != 0 || len(out.Attempts) != 2 {
		t.Fatalf("malformed trace not bounded: %+v", out)
	}
	if a.Mode != "unknown" || a.Outcome != "unknown" || a.Stage != "unknown" || a.Status != 0 || a.DurationMS != 3600000 || a.Bytes != 0 || *a.Matches != 0 || *a.Items != 1000 || a.Warnings == nil {
		t.Fatalf("malformed attempt not bounded: %+v", a)
	}
	if out.Attempts[1].Matches != nil || out.Attempts[1].Items != nil {
		t.Fatal("unknown extraction counts became zero")
	}
}
