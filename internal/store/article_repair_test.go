package store

import (
	"context"
	"testing"

	"rss-workshop/internal/model"
)

func articleFeed(selector string) model.Feed {
	r := model.Recipe{Type: "css", Items: "article", Title: model.Field{Selector: "h2"}}
	if selector != "" {
		r.Full = &model.FullContent{Selector: selector}
	}
	return model.Feed{Title: "Article feed", URL: "https://example.com/news", Recipe: r, Interval: 900}
}

// An article body is fetched once and the item merge preserves it, so a
// selector that captured the wrong block would outlive every later refresh and
// every edit. Editing the selector has to be the repair path.
func TestEditingTheArticleSelectorClearsStoredBodies(t *testing.T) {
	ctx := context.Background()
	for name, edit := range map[string]func(model.Feed) model.Feed{
		"different selector": func(f model.Feed) model.Feed {
			f.Recipe.Full = &model.FullContent{Selector: ".correct-body"}
			return f
		},
		"different attribute": func(f model.Feed) model.Feed {
			f.Recipe.Full = &model.FullContent{Selector: ".body", Attr: "data-article"}
			return f
		},
		"feature turned off": func(f model.Feed) model.Feed {
			f.Recipe.Full = nil
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := testStoreOpener(t, 50)()
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			id, err := s.Save(ctx, articleFeed(".body"))
			if err != nil {
				t.Fatal(err)
			}
			saved, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			stored := []model.Item{{Key: "k", Title: "Story", URL: "https://example.com/a",
				HTML: "<p>teaser</p>", FullHTML: "<nav>the wrong block</nav>"}}
			if err := s.Complete(ctx, saved, stored, "", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			before, err := s.Items(ctx, id)
			if err != nil || len(before) != 1 || before[0].FullHTML == "" {
				t.Fatalf("setup did not store an article body: %v", err)
			}

			edited, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Save(ctx, edit(edited)); err != nil {
				t.Fatal(err)
			}
			after, err := s.Items(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != 1 {
				t.Fatalf("the edit removed the story: %d items", len(after))
			}
			if after[0].FullHTML != "" {
				t.Errorf("the wrong article body survived the edit: %q", after[0].FullHTML)
			}
			// The story itself, its identity and its teaser must be untouched.
			if after[0].HTML != "<p>teaser</p>" || after[0].GUID != before[0].GUID || !after[0].Published.Equal(before[0].Published) {
				t.Error("clearing the article body disturbed the story")
			}
		})
	}
}

// An unrelated edit must not throw away work that is still correct.
func TestUnrelatedEditsKeepStoredArticleBodies(t *testing.T) {
	ctx := context.Background()
	for name, edit := range map[string]func(model.Feed) model.Feed{
		"title":    func(f model.Feed) model.Feed { f.Title = "Renamed"; return f },
		"interval": func(f model.Feed) model.Feed { f.Interval = 1800; return f },
		"browser toggle": func(f model.Feed) model.Feed {
			f.Recipe.Full = &model.FullContent{Selector: ".body", Browser: true}
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := testStoreOpener(t, 50)()
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			id, err := s.Save(ctx, articleFeed(".body"))
			if err != nil {
				t.Fatal(err)
			}
			saved, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Complete(ctx, saved, []model.Item{{Key: "k", Title: "Story", FullHTML: "<p>the right article</p>"}}, "", "", 200, nil, 0); err != nil {
				t.Fatal(err)
			}
			edited, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Save(ctx, edit(edited)); err != nil {
				t.Fatal(err)
			}
			after, err := s.Items(ctx, id)
			if err != nil || len(after) != 1 {
				t.Fatal(err)
			}
			if after[0].FullHTML != "<p>the right article</p>" {
				t.Errorf("an unrelated edit discarded a correct article body: %q", after[0].FullHTML)
			}
		})
	}
}
