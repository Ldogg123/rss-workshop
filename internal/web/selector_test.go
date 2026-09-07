package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

func TestSelectorSnapshotBoundary(t *testing.T) {
	source := `<html><head><base href="http://127.0.0.1/"><style>body{background:url(http://127.0.0.1/css-attack)}</style></head><body onload="sourceAttack()"><script id="hidden-script" class="hidden-class">scriptAttack()</script><iframe src="http://127.0.0.1/frame-attack" srcdoc="iframeAttack()"></iframe><object data="http://127.0.0.1/object-attack"></object><article class="card"><h2 id="parent">A &amp; B</h2><a href="javascript:linkAttack()" onclick="clickAttack()">Read</a><img src="/thumbnail.jpg" data-src="http://127.0.0.1/image-attribute" onerror="errorAttack()" alt="Thumbnail"><time datetime="2026-01-01">Today</time><svg onload="svgAttack()"><text>SVG attack</text></svg></article></body></html>`
	nodes, err := selectorTree([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(nodes)
	for _, forbidden := range []string{"Attack()", "css-attack", "frame-attack", "object-attack", "hidden-script", "hidden-class", "javascript:", "SVG attack", "onload", "srcdoc"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("active content leaked: %s", forbidden)
		}
	}
	tags := map[string]selectorNode{}
	for i, n := range nodes {
		if n.Parent >= i {
			t.Fatal("invalid tree parent")
		}
		if n.Tag != "" {
			tags[n.Tag] = n
		}
	}
	if tags["article"].Class != "card" || tags["h2"].ID != "parent" || !tags["time"].Datetime || !tags["a"].Link || tags["img"].Alt != "Thumbnail" {
		t.Fatal("lost selection metadata")
	}
	if !tags["script"].Hidden || !tags["iframe"].Hidden {
		t.Fatal("unsafe elements should preserve only their sibling positions")
	}
	if tags["a"].Attrs["href"] != "" || tags["img"].Attrs["src"] != "/thumbnail.jpg" || tags["img"].Attrs["data-src"] != "http://127.0.0.1/image-attribute" {
		t.Fatal("expected inert resource attributes without script URLs")
	}
	for _, n := range nodes {
		if n.Hidden && (n.ID != "" || n.Class != "" || n.TestID != "" || n.Alt != "" || n.Link || n.Datetime || len(n.Attrs) != 0) {
			t.Fatal("hidden root retained source metadata", n)
		}
	}
	if _, err := selectorTree([]byte(`<invalid@tag>text</invalid@tag>`)); err == nil {
		t.Fatal("unsupported DOM names must return a usable error")
	}
	for name, source := range map[string]string{"bytes": strings.Repeat("x", (4<<20)+1), "nodes": strings.Repeat("<p>x</p>", 6000), "depth": strings.Repeat("<div>", 110)} {
		t.Run(name, func(t *testing.T) {
			if _, err := selectorTree([]byte(source)); err == nil {
				t.Fatal("snapshot limit not enforced")
			}
		})
	}
}

func TestSelectorAttributesAndText(t *testing.T) {
	source := "<article id=story class='card lead' data-testid=card data-category=world aria-label='A &amp; B' role=listitem lang=en itemprop=headline> \n <h2 title='Headline title'> A\n B </h2> \t <a href='../story?q=one&amp;page=2' rel=bookmark>Read</a>\n<img src='https://example.com/a.jpg' srcset='/a.jpg 1x, /b.jpg 2x' data-lazy-src='/lazy.jpg' alt='A &amp; B' loading=lazy><time datetime='2026-09-07T12:00:00Z'>Today</time></article>"
	nodes, err := selectorTree([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	article := -1
	tags := map[string]selectorNode{}
	for i, n := range nodes {
		if n.Tag == "article" {
			article = i
		}
		if n.Tag != "" {
			tags[n.Tag] = n
		}
	}
	wantAttrs := map[string]string{"id": "story", "class": "card lead", "data-testid": "card", "data-category": "world", "aria-label": "A & B", "role": "listitem", "lang": "en", "itemprop": "headline"}
	if !reflect.DeepEqual(tags["article"].Attrs, wantAttrs) {
		t.Fatalf("article attributes = %#v, want %#v", tags["article"].Attrs, wantAttrs)
	}
	if tags["article"].ID != "story" || tags["article"].Class != "card lead" || tags["article"].TestID != "card" {
		t.Fatal("compatibility metadata changed")
	}
	for tag, attrs := range map[string]map[string]string{
		"h2":   {"title": "Headline title"},
		"a":    {"href": "../story?q=one&page=2", "rel": "bookmark"},
		"img":  {"src": "https://example.com/a.jpg", "srcset": "/a.jpg 1x, /b.jpg 2x", "data-lazy-src": "/lazy.jpg", "alt": "A & B", "loading": "lazy"},
		"time": {"datetime": "2026-09-07T12:00:00Z"},
	} {
		if !reflect.DeepEqual(tags[tag].Attrs, attrs) {
			t.Errorf("%s attributes = %#v, want %#v", tag, tags[tag].Attrs, attrs)
		}
	}
	var children []string
	for _, n := range nodes {
		if n.Parent == article {
			if n.Tag != "" {
				children = append(children, "<"+n.Tag+">")
			} else {
				children = append(children, n.Text)
			}
		}
	}
	wantChildren := []string{" \n ", "<h2>", " \t ", "<a>", "\n", "<img>", "<time>"}
	if !reflect.DeepEqual(children, wantChildren) {
		t.Fatalf("article children = %#v, want %#v", children, wantChildren)
	}
	foundHeadline := false
	for _, n := range nodes {
		foundHeadline = foundHeadline || n.Text == " A\n B "
	}
	if !foundHeadline {
		t.Fatal("text content whitespace changed")
	}
}

func TestSelectorAttributeLimits(t *testing.T) {
	var source strings.Builder
	source.WriteString(`<article data-valid="yes" data-invalid@name="no" xml:lang="no" xmlns="no" style="display:none" srcdoc="no" onclick="no" href="java&#10;script:bad()" src=" VBScript:bad()" srcset="/safe.jpg 1x, javascript:bad() 2x" data-src="javascript:bad()" data-exact="`)
	source.WriteString(strings.Repeat("x", 2048))
	source.WriteString(`" data-oversize="`)
	source.WriteString(strings.Repeat("x", 2049))
	source.WriteString(`" data-` + strings.Repeat("x", 125) + `="oversize name"`)
	for i := 0; i < maxSelectorAttrs+10; i++ {
		fmt.Fprintf(&source, ` data-extra-%d="%d"`, i, i)
	}
	source.WriteString(`></article>`)
	nodes, err := selectorTree([]byte(source.String()))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Tag != "article" {
			continue
		}
		if len(n.Attrs) != maxSelectorAttrs || n.Attrs["data-valid"] != "yes" || len(n.Attrs["data-exact"]) != 2048 {
			t.Fatalf("attribute limits lost valid metadata: %#v", n.Attrs)
		}
		for key, value := range n.Attrs {
			if key != "data-valid" && key != "data-exact" && !strings.HasPrefix(key, "data-extra-") {
				t.Fatalf("unsupported attribute retained: %s=%q", key, value)
			}
		}
		return
	}
	t.Fatal("article not found")
}

func TestSelectorEndpointAuthAndFramePolicy(t *testing.T) {
	authn, err := auth.New("selector-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fixtureFetcher{body: []byte(`<article><h2>Story</h2></article>`)}
	app := &App{Auth: authn, BaseURL: "http://localhost:8080", Scheduler: scheduler.New(nil, fetcher, 1, time.Second)}
	handler := app.Handler()
	session, err := authn.Login("selector-test-password")
	if err != nil {
		t.Fatal(err)
	}
	cookieResponse := httptest.NewRecorder()
	authn.Cookie(cookieResponse, session)
	request := func(signed, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/selector/snapshot", strings.NewReader(`{"url":"https://example.com/news","recipe":{"mode":"static"}}`))
		r.Header.Set("Origin", app.BaseURL)
		r.Header.Set("Content-Type", "application/json")
		if signed {
			r.AddCookie(cookieResponse.Result().Cookies()[0])
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", session.CSRF)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(false, false); w.Code != 401 {
		t.Fatal("snapshot accessible without session", w.Code)
	}
	if w := request(true, false); w.Code != 403 {
		t.Fatal("snapshot accessible without CSRF", w.Code)
	}
	if fetcher.calls.Load() != 0 {
		t.Fatal("rejected requests fetched source")
	}
	if w := request(true, true); w.Code != 200 || !strings.Contains(w.Body.String(), "Story") {
		t.Fatal("snapshot failed", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/selector/frame", nil))
	policy := w.Header().Get("Content-Security-Policy")
	if w.Code != 200 || !strings.Contains(policy, "sandbox allow-scripts") || !strings.Contains(policy, "default-src 'none'") || strings.Contains(policy, "allow-same-origin") || strings.Contains(policy, "unsafe-inline") {
		t.Fatal("weak frame policy", policy)
	}
	if w.Header().Get("X-Frame-Options") != "" || !strings.Contains(policy, "frame-ancestors 'self'") {
		t.Fatal("frame cannot be embedded by app")
	}
	if strings.Contains(w.Body.String(), "Story") {
		t.Fatal("public frame contains source data")
	}
	if active, _ := app.Scheduler.Stats(); active != 0 {
		t.Fatal("snapshot leaked slot")
	}
	_, err = app.Scheduler.Snapshot(context.Background(), "https://example.com", model.Recipe{Mode: "browser"})
	if err == nil {
		t.Fatal("missing browser accepted")
	}
}
