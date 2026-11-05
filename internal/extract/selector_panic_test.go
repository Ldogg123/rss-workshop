package extract

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"rss-workshop/internal/model"
)

// The numeric comparison panics in antchfx/xpath up to v1.3.7 and substring up
// to v1.3.8, both on ordinary page content. Moving past them changes which
// error these cases report, so update the expectation with that move; the form
// in alwaysPanics keeps exercising the guard afterwards.
const (
	numericComparison = `@data-score > 1`
	shortSubstring    = `substring(@href,2,5)='a'`
)

// alwaysPanics still panics in xpath v1.3.9, whatever the page holds.
const alwaysPanics = `//article[sum('x') > 0]`

const secretValue = "https://source.example/private?token=secret"

func panicRecipe() model.Recipe {
	return model.Recipe{Type: "xpath", Items: "//article", Title: model.Field{Selector: ".//h2"}, Link: model.Field{Selector: ".//a/@href"}}
}

func TestSelectorPanicsBecomeErrors(t *testing.T) {
	page := `<article data-score="n/a"><h2 data-score="n/a">Story</h2><a data-score="n/a" href="/a">x</a><p data-score="n/a">Text</p><img data-score="n/a" src="/i.png"><time data-score="n/a">2026-01-02</time></article>`
	for _, tc := range []struct {
		name, label string
		edit        func(*model.Recipe)
	}{
		{"items comparison", "Items", func(r *model.Recipe) { r.Items = `//article[` + numericComparison + `]` }},
		{"items substring", "Items", func(r *model.Recipe) { r.Items = `//article[substring(.//a/@href,2,5)='a']` }},
		{"items regardless of version", "Items", func(r *model.Recipe) { r.Items = alwaysPanics }},
		{"title", "Title", func(r *model.Recipe) { r.Title.Selector = `.//h2[` + numericComparison + `]` }},
		{"link", "Link", func(r *model.Recipe) { r.Link.Selector = `.//a[` + shortSubstring + `]` }},
		{"content", "Content", func(r *model.Recipe) { r.Content.Selector = `.//p[` + numericComparison + `]` }},
		{"image", "Image", func(r *model.Recipe) { r.Image.Selector = `.//img[` + numericComparison + `]` }},
		{"date", "Date", func(r *model.Recipe) { r.Date.Selector = `.//time[` + numericComparison + `]` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := panicRecipe()
			tc.edit(&r)
			if err := Validate(r); err != nil {
				t.Fatalf("the recipe must pass validation to reach evaluation: %v", err)
			}
			p, err := Run([]byte(page), "https://example.com/", r)
			if !errors.Is(err, ErrSelectorFailed) {
				t.Fatalf("got %v", err)
			}
			// Only a page with no usable stories may try the browser.
			if errors.Is(err, ErrNoItems) {
				t.Fatal("a failed selector must not count as an empty page")
			}
			if !strings.Contains(err.Error(), tc.label) {
				t.Fatalf("error does not name the %s field: %v", tc.label, err)
			}
			if len(p.Items) != 0 {
				t.Fatalf("a failed extraction returned items: %+v", p.Items)
			}
		})
	}
}

// A card further down the page is enough to fail it, and the stories read
// before that card must not look like a usable result.
func TestSelectorPanicOnALaterCardDiscardsEarlierItems(t *testing.T) {
	page := `<article><h2>First</h2><a href="/ok/long/enough">x</a></article><article><h2>Second</h2><a href="/a">x</a></article>`
	r := panicRecipe()
	r.Link.Selector = `.//a[` + shortSubstring + `]`
	p, err := Run([]byte(page), "https://example.com/", r)
	if !errors.Is(err, ErrSelectorFailed) {
		t.Fatalf("got %v", err)
	}
	if len(p.Items) != 0 || p.Matches != 2 {
		t.Fatalf("got %d items from %d matches", len(p.Items), p.Matches)
	}
}

func TestArticleSelectorPanicsBecomeErrors(t *testing.T) {
	for _, tc := range []struct {
		name, typ, selector, page string
	}{
		{"comparison", "xpath", `//main[@data-score > 1]`, `<main data-score="` + secretValue + `">Body</main>`},
		{"substring", "xpath", `//main[substring(.//a/@href,2,5)='a']`, `<main><a href="/a">Body</a></main>`},
		{"regardless of version", "xpath", `//main[sum('x') > 0]`, `<main>Body</main>`},
		// Article does not revalidate, so a stored selector the current library
		// no longer compiles reaches goquery with no matcher.
		{"css that no longer compiles", "css", `[`, `<main>Body</main>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := model.Recipe{Type: tc.typ, Full: &model.FullContent{Selector: tc.selector}}
			body, err := Article([]byte(tc.page), "https://example.com/story", r)
			if !errors.Is(err, ErrSelectorFailed) {
				t.Fatalf("got %v", err)
			}
			if body != "" || strings.Contains(err.Error(), "source.example") {
				t.Fatalf("body %q, error %v", body, err)
			}
		})
	}
}

// A panic's value quotes the page: xpath's numeric comparison reports
// strconv.ParseFloat: parsing "<the attribute>". Attributes hold URLs, and
// selectors can hold them too, so neither may reach an error or the log.
func TestSelectorPanicKeepsPageValuesAndSelectorsOutOfLogs(t *testing.T) {
	var log bytes.Buffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))

	r := panicRecipe()
	r.Items = `//article[@data-score > 1][contains(@id,'https://selector.example/')]`
	_, err := Run([]byte(`<article data-score="`+secretValue+`"><h2>Story</h2></article>`), "https://example.com/", r)
	if !errors.Is(err, ErrSelectorFailed) {
		t.Fatalf("got %v", err)
	}
	for _, leaked := range []string{"source.example", "selector.example", "token=secret"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error contains %q: %v", leaked, err)
		}
		if strings.Contains(log.String(), leaked) {
			t.Errorf("log contains %q:\n%s", leaked, log.String())
		}
	}
	// What is left still has to identify the library bug for a report.
	for _, want := range []string{`msg="selector library panicked"`, "selector=Items", "panic_type=*strconv.NumError", "antchfx/xpath"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
}

// A page controls how often this line is written, so it must stay bounded
// however deep the failure happened.
func TestSelectorPanicLogLineIsBounded(t *testing.T) {
	var log bytes.Buffer
	restore := slog.Default()
	defer slog.SetDefault(restore)
	slog.SetDefault(slog.New(slog.NewTextHandler(&log, nil)))

	var deep func(int) error
	deep = func(n int) error {
		if n == 0 {
			return selectorFailure("Items", fmt.Errorf("value %s", secretValue))
		}
		return deep(n - 1)
	}
	if err := deep(5000); !errors.Is(err, ErrSelectorFailed) {
		t.Fatalf("got %v", err)
	}
	// Quoting doubles some bytes, and the line has a few other short fields.
	if got, limit := log.Len(), 2*maxPanicStack+1024; got > limit {
		t.Fatalf("log line is %d bytes, over %d", got, limit)
	}
	if strings.Contains(log.String(), "source.example") {
		t.Fatalf("log contains the panic's value:\n%s", log.String())
	}
}

// htmlquery caches compiled expressions and shares them between feeds, so a
// panic part-way through one evaluation must not affect the next one.
func TestSelectorPanicLeavesTheSelectorUsable(t *testing.T) {
	r := panicRecipe()
	r.Items = `//article[@data-score > 1]`
	good := []byte(`<article data-score="2"><h2>Story</h2><a href="/s">x</a></article>`)
	bad := []byte(`<article data-score="n/a"><h2>Story</h2><a href="/s">x</a></article>`)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				if _, err := Run(bad, "https://example.com/", r); !errors.Is(err, ErrSelectorFailed) {
					t.Errorf("bad page: %v", err)
				}
				if p, err := Run(good, "https://example.com/", r); err != nil || len(p.Items) != 1 {
					t.Errorf("good page: %d items, %v", len(p.Items), err)
				}
			}
		}()
	}
	wg.Wait()
}
