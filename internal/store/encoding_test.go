package store

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
)

func TestMalformedSourceTextAndOpaqueBytesPersist(t *testing.T) {
	for _, selectorType := range []string{"css", "xpath"} {
		t.Run(selectorType, func(t *testing.T) {
			ctx := t.Context()
			open := testStoreOpener(t, 10)
			s, err := open()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.DB.Close() })
			r := model.Recipe{Type: selectorType, Items: "article", Title: model.Field{Selector: "h2"}, Content: model.Field{Selector: "p"}}
			if selectorType == "xpath" {
				r.Items, r.Title.Selector, r.Content.Selector = "//article", ".//h2", ".//p"
			}
			// A static HTML response may contain Latin-1 or malformed UTF-8.
			// No link selector makes the extracted identity depend on the raw
			// title bytes, so normalization must not recompute that identity.
			preview, err := extract.Run([]byte("<article><h2>Caf\xe9</h2><p>Legacy \xff text</p></article>"), "https://example.com", r)
			if err != nil || len(preview.Items) != 1 {
				t.Fatal("could not extract legacy-encoded fixture", err)
			}
			if utf8.ValidString(preview.Items[0].Title) {
				t.Fatal("fixture no longer reproduces invalid UTF-8 at the storage boundary")
			}
			f := model.Feed{Title: "Feed\xff\x00", URL: "https://example.com", Recipe: r, Interval: 300, Enabled: true}
			f.ID, err = s.Save(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			f, err = s.Get(ctx, f.ID)
			if err != nil || f.Title != "Feed\uFFFD\uFFFD" {
				t.Fatal("feed title was not made readable", err)
			}
			key := "opaque:\x80\x00\\\xff"
			items := append(preview.Items, model.Item{
				Key: key, Title: "Direct\xff\x00", HTML: "<p>Bad\xff\x00</p>",
				URL: "https://example.com/\xff\x00", Image: "https://example.com/image\xff\x00",
			})
			etag, modified := "\"etag:\x80\x00\\\xff\"", "date:\xff\x00"
			started := time.Now().Truncate(time.Second)
			if err := s.Complete(ctx, f, items, etag, modified, 200, nil, 0); err != nil {
				t.Fatal("malformed source prevented successful refresh", err)
			}
			f, err = s.Get(ctx, f.ID)
			if err != nil || f.LastSuccess.Before(started) || f.LastAttempt.Before(started) || f.NextRun.Before(started.Add(300*time.Second)) || f.Error != "" || f.Failures != 0 || f.ETag != etag || f.LastModified != modified {
				t.Fatal("successful refresh lost schedule or opaque validators", err)
			}
			stored, err := s.Items(ctx, f.ID)
			if err != nil || len(stored) != 2 {
				t.Fatal("malformed source did not persist", err)
			}
			byKey := relativeDatesByKey(t, stored)
			if byKey[preview.Items[0].Key].Title != "Caf\uFFFD" || byKey[key].Title != "Direct\uFFFD\uFFFD" || byKey[key].HTML != "<p>Bad\uFFFD\uFFFD</p>" || byKey[key].URL != "https://example.com/\uFFFD\uFFFD" || byKey[key].Image != "https://example.com/image\uFFFD\uFFFD" {
				t.Fatal("source text was not made readable, or original key bytes changed")
			}
			for _, item := range stored {
				wantGUID := fmt.Sprintf("urn:sha256:%x", sha256.Sum256([]byte(f.ID+"\x00"+item.Key)))
				if item.GUID != wantGUID {
					t.Fatal("opaque key normalization changed its GUID")
				}
				for _, value := range []string{item.Title, item.HTML, item.URL, item.Image} {
					if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
						t.Fatal("persisted display text still contains invalid encoding")
					}
				}
			}
			// Repeated extraction must merge into the original identity, and
			// a later failure must record backoff rather than leave it due.
			items[1].Title = "Updated\xff"
			if err := s.Complete(ctx, f, items, etag, modified, 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			merged, err := s.Items(ctx, f.ID)
			if err != nil || len(merged) != 2 {
				t.Fatal("opaque identity did not merge", err)
			}
			relativeDatesAssertStable(t, byKey, relativeDatesByKey(t, merged))
			started = time.Now().Truncate(time.Second)
			if err := s.Complete(ctx, f, nil, "replacement", "replacement", 500, errors.New("failure:\xff\x00"), 10*time.Minute); err != nil {
				t.Fatal("malformed failure prevented backoff", err)
			}
			failed, err := s.Get(ctx, f.ID)
			if err != nil || failed.Failures != 1 || failed.Error != "failure:\uFFFD\uFFFD" || failed.NextRun.Before(started.Add(10*time.Minute)) || failed.ETag != etag || failed.LastModified != modified {
				t.Fatal("failure did not preserve validators and advance schedule", err)
			}
			if err := s.DB.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = open()
			if err != nil {
				t.Fatal(err)
			}
			reopenedFeed, err := s.Get(ctx, f.ID)
			if err != nil || reopenedFeed.ETag != etag || reopenedFeed.LastModified != modified {
				t.Fatal("restart lost opaque validators", err)
			}
			reopenedItems, err := s.Items(ctx, f.ID)
			if err != nil || !reflect.DeepEqual(reopenedItems, merged) {
				t.Fatal("restart changed opaque identities or saved items", err)
			}
			// Edits still clear BYTEA validators; imported display text uses
			// the same normalization without changing the recipe JSON.
			reopenedFeed.Title = "Edited\xff\x00"
			if _, err := s.Save(ctx, reopenedFeed); err != nil {
				t.Fatal(err)
			}
			edited, err := s.Get(ctx, f.ID)
			if err != nil || edited.Title != "Edited\uFFFD\uFFFD" || edited.ETag != "" || edited.LastModified != "" {
				t.Fatal("edit failed to clear opaque validators", err)
			}
			ids, err := s.Import(ctx, []model.Feed{{Title: "Imported\xff\x00", URL: "https://example.com/\xff\x00", Recipe: r, Interval: 60}})
			if err != nil || len(ids) != 1 {
				t.Fatal("malformed imported display text was rejected", err)
			}
			imported, err := s.Get(ctx, ids[0])
			if err != nil || imported.Title != "Imported\uFFFD\uFFFD" || imported.URL != "https://example.com/\uFFFD\uFFFD" || !reflect.DeepEqual(imported.Recipe, r) {
				t.Fatal("import text normalization changed configuration", err)
			}
		})
	}
}
