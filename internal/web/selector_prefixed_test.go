package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"rss-workshop/internal/auth"
	"rss-workshop/internal/extract"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
)

// A page pasted from Word, with a social widget between two stories. Every
// prefixed element carries something that must stay on the server: a name, an
// attribute, text or a child element.
const prefixedPage = `<html><head><title>Parish news</title></head><body>
<h1>Parish news</h1>
<p class="intro">Notes from <st1:City w:st="on">Smart-tag-text</st1:City> this week<o:p></o:p></p>
<div class="WordSection1">
<p class="MsoNormal"><a href="/one">First story</a><o:p></o:p></p>
<o:p>&nbsp;</o:p>
<p class="MsoNormal"><a href="/two">Second story</a><o:p></o:p></p>
<fb:like href="https://example.com/liked" class="like-class" onclick="likeAttack()">Like-text<span id="inside-like">Inner-text</span></fb:like>
<p class="MsoNormal"><a href="/three">Third story</a><o:p></o:p></p>
</div>
</body></html>`

func prefixedTree(t *testing.T) []selectorNode {
	t.Helper()
	nodes, err := selectorTree([]byte(prefixedPage))
	if err != nil {
		t.Fatalf("a page with prefixed elements did not open: %v", err)
	}
	return nodes
}

func TestSelectorTreeKeepsPrefixedElementsAsPlaceholders(t *testing.T) {
	nodes := prefixedTree(t)
	b, err := json.Marshal(nodes)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"o:p", "st1", "fb:like", "w:st", "liked", "like-class", "likeAttack", "Like-text", "inside-like", "Inner-text", "Smart-tag-text", " "} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("a prefixed element's name, attributes or content reached the snapshot: %q", forbidden)
		}
	}
	placeholders := 0
	for i, n := range nodes {
		if n.Parent >= i {
			t.Fatal("invalid tree parent")
		}
		if n.Parent >= 0 && nodes[n.Parent].Tag == selectorPlaceholder {
			t.Fatalf("node %d was kept inside a placeholder: %#v", i, n)
		}
		if n.Tag != selectorPlaceholder {
			continue
		}
		placeholders++
		if !reflect.DeepEqual(n, selectorNode{Parent: n.Parent, Tag: selectorPlaceholder, Hidden: true}) {
			t.Fatalf("a placeholder carries more than its position: %#v", n)
		}
	}
	// st1:City, four o:p inside paragraphs, one o:p between them, fb:like.
	if placeholders != 7 {
		t.Fatalf("placeholders = %d, want 7", placeholders)
	}
	section := -1
	for i, n := range nodes {
		if n.Class == "WordSection1" {
			section = i
		}
	}
	var children []string
	for _, n := range nodes {
		if n.Parent == section && n.Tag != "" {
			children = append(children, n.Tag)
		}
	}
	if want := []string{"p", selectorPlaceholder, "p", selectorPlaceholder, "p"}; !reflect.DeepEqual(children, want) {
		t.Fatalf("element children = %q, want %q", children, want)
	}
}

// The placeholder's name is what keeps :nth-of-type and XPath name tests
// right for its neighbours: it must be a name the preview frame can create,
// and one no element of the page can be sent under.
func TestSelectorPlaceholderNameIsNoPageElement(t *testing.T) {
	if selectorTag.MatchString(selectorPlaceholder) {
		t.Fatalf("%q is a name a page element can be sent under", selectorPlaceholder)
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`).MatchString(selectorPlaceholder) {
		t.Fatalf("%q is not an XML element name", selectorPlaceholder)
	}
	// An element the page itself names like the placeholder is one too, so
	// the name never stands for two different elements.
	nodes, err := selectorTree([]byte(`<div><` + selectorPlaceholder + ` id="own">text</` + selectorPlaceholder + `><p>after</p></div>`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(nodes)
	if strings.Contains(string(b), "own") || !strings.Contains(string(b), `"`+selectorPlaceholder+`"`) {
		t.Fatalf("an element named like the placeholder was sent as itself: %s", b)
	}
}

// Names the HTML parser accepts but the mirror cannot carry are not always
// prefixes: an address in angle brackets is read as a tag as well.
func TestSelectorTreeKeepsOtherUnsupportedNamesAsPlaceholders(t *testing.T) {
	for _, source := range []string{
		`<p>Write to <editor@example.com></p><p>after</p>`,
		`<p>before</p><my_widget data-secret="x">inside</my_widget><p>after</p>`,
		`<p>before</p><a:b:c>inside</a:b:c><p>after</p>`,
		`<p>See <http://example.com/page></p><p>after</p>`,
	} {
		nodes, err := selectorTree([]byte(source))
		if err != nil {
			t.Fatalf("%s: %v", source, err)
		}
		b, _ := json.Marshal(nodes)
		for _, forbidden := range []string{"example.com", "my_widget", "data-secret", "a:b:c", "inside", "http:"} {
			if strings.Contains(string(b), forbidden) {
				t.Errorf("%s: %q reached the snapshot", source, forbidden)
			}
		}
		if !strings.Contains(string(b), `"`+selectorPlaceholder+`"`) || !strings.Contains(string(b), `"after"`) {
			t.Errorf("%s: lost the placeholder or the content after it: %s", source, b)
		}
	}
}

// Placeholders are nodes like any other, so a page made of them still meets
// the node and depth limits.
func TestSelectorPlaceholdersCountTowardsLimits(t *testing.T) {
	for name, source := range map[string]string{
		"nodes": strings.Repeat("<o:p></o:p>", 12000),
		"depth": strings.Repeat("<div>", 98) + "<o:p></o:p>",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := selectorTree([]byte(source)); err == nil || !strings.Contains(err.Error(), "too complex") {
				t.Fatalf("limit not enforced: %v", err)
			}
		})
	}
	for _, source := range []string{strings.Repeat("<o:p></o:p>", 11000), strings.Repeat("<div>", 98)} {
		if _, err := selectorTree([]byte(source)); err != nil {
			t.Fatalf("a page within the limits was refused: %v", err)
		}
	}
}

// The picker's positional suggestions come from sibling positions in the
// snapshot (exact() in selector-frame.js counts siblings of the same name),
// and hand-written :nth-child or *[n] selectors are highlighted from the same
// tree. Extraction parses the real page, where a prefixed element is an
// element with a name of its own. Rebuild those paths from the snapshot and
// run them through the extractor: each must find the paragraph it was built
// for. Leaving the element out of the snapshot would shift :nth-child, and a
// placeholder called p would shift :nth-of-type.
func TestSelectorPlaceholderKeepsNeighbourPositions(t *testing.T) {
	nodes := prefixedTree(t)
	sameName := func(i int) int {
		k := 1
		for j := 0; j < i; j++ {
			if nodes[j].Parent == nodes[i].Parent && nodes[j].Tag == nodes[i].Tag {
				k++
			}
		}
		return k
	}
	anyName := func(i int) int {
		k := 1
		for j := 0; j < i; j++ {
			if nodes[j].Parent == nodes[i].Parent && nodes[j].Tag != "" {
				k++
			}
		}
		return k
	}
	path := func(i int, step func(int) string, separator string) string {
		var parts []string
		for ; i >= 0; i = nodes[i].Parent {
			parts = append([]string{step(i)}, parts...)
		}
		return strings.Join(parts, separator)
	}
	for wantNthChild, title := range map[int]string{1: "First story", 3: "Second story", 5: "Third story"} {
		story := -1
		for _, n := range nodes {
			if n.Text == title {
				story = nodes[n.Parent].Parent // text, in a, in p
			}
		}
		if story < 0 || nodes[story].Tag != "p" {
			t.Fatalf("%s: paragraph not found in the snapshot", title)
		}
		if got := anyName(story); got != wantNthChild {
			t.Fatalf("%s: snapshot position = %d, want %d", title, got, wantNthChild)
		}
		for name, recipe := range map[string]model.Recipe{
			"css nth-of-type": {Type: "css", Title: model.Field{Selector: "a"},
				Items: path(story, func(i int) string { return fmt.Sprintf("%s:nth-of-type(%d)", nodes[i].Tag, sameName(i)) }, " > ")},
			"css nth-child": {Type: "css", Title: model.Field{Selector: "a"},
				Items: path(story, func(i int) string { return fmt.Sprintf("%s:nth-child(%d)", nodes[i].Tag, anyName(i)) }, " > ")},
			"xpath name[n]": {Type: "xpath", Title: model.Field{Selector: ".//a"},
				Items: "//" + path(story, func(i int) string { return fmt.Sprintf("%s[%d]", nodes[i].Tag, sameName(i)) }, "/")},
			"xpath *[n]": {Type: "xpath", Title: model.Field{Selector: ".//a"},
				Items: "/" + path(story, func(i int) string { return fmt.Sprintf("*[%d]", anyName(i)) }, "/")},
		} {
			recipe.Mode = "static"
			out, err := extract.Run([]byte(prefixedPage), "https://example.com/news", recipe)
			if err != nil {
				t.Fatalf("%s, %s (%s): %v", title, name, recipe.Items, err)
			}
			if len(out.Items) != 1 || out.Items[0].Title != title {
				t.Errorf("%s, %s: %s extracted %d items %v from the real page", title, name, recipe.Items, len(out.Items), out.Items)
			}
			// The two paths the picker suggests for this paragraph, as the
			// browser test reads them from its suggestions.
			if want := map[string]string{"css nth-of-type": "html:nth-of-type(1) > body:nth-of-type(1) > div:nth-of-type(1) > p:nth-of-type(3)", "xpath name[n]": "//html[1]/body[1]/div[1]/p[3]"}[name]; title == "Third story" && want != "" && recipe.Items != want {
				t.Errorf("%s: path = %s, want %s", name, recipe.Items, want)
			}
		}
	}
}

func TestSelectorSnapshotOpensPageWithPrefixedElements(t *testing.T) {
	authn, err := auth.New("selector-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Auth: authn, BaseURL: "http://localhost:8080", Scheduler: scheduler.New(nil, &fixtureFetcher{body: []byte(prefixedPage)}, 1, time.Second)}
	session, err := authn.Login("selector-test-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := httptest.NewRecorder()
	authn.Cookie(cookie, session)
	r := httptest.NewRequest("POST", "/api/selector/snapshot", strings.NewReader(`{"url":"https://example.com/news","recipe":{"mode":"static"}}`))
	r.Header.Set("Origin", app.BaseURL)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", session.CSRF)
	r.AddCookie(cookie.Result().Cookies()[0])
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("snapshot = %d %s", w.Code, w.Body.String())
	}
	var reply struct {
		Nodes []selectorNode `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if want := prefixedTree(t); !reflect.DeepEqual(reply.Nodes, want) {
		t.Fatal("the snapshot is not the page's selector tree")
	}
	for _, forbidden := range []string{"o:p", "fb:like", "likeAttack", "Like-text", "Smart-tag-text"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Errorf("%q reached the browser", forbidden)
		}
	}
}

// An unknown element the page never closes takes everything up to the end of
// its parent as its children, so its placeholder hides stories that are on
// the page. The preview cannot show them, but the snapshot must say so.
const (
	unclosedWidgetPage = `<body><div id="main"><fb:like href="https://example.com/liked" /><article><h2>Swallowed-one</h2></article><article><h2>Swallowed-two</h2></article></div></body>`
	// What Word writes: empty or blank <o:p>, smart tags around a word, and
	// an <o:p/> the paragraph's own end tag closes.
	wordOnlyPage = `<body><h1>Parish news</h1>
<p class="MsoNormal">Notes from <st1:place w:st="on"><st1:City w:st="on">Paris</st1:City></st1:place> this week<o:p></o:p></p>
<o:p>&nbsp;</o:p>
<p class="MsoNormal"><a href="/one">First story</a><o:p/></p>
<p class="MsoNormal"><a href="/two">Second story</a><o:p></o:p></p>
</body>`
)

func TestSelectorTreeCountsPlaceholdersThatHideElements(t *testing.T) {
	for name, c := range map[string]struct {
		source string
		want   int
	}{
		"unclosed widget before the stories": {unclosedWidgetPage, 1},
		"address typed as a tag":             {`<body><h1>News</h1>Contact <editor@example.com> for details.<article><h2>One</h2></article><article><h2>Two</h2></article></body>`, 1},
		"unclosed o:p between paragraphs":    {`<div class="news"><p class="s">One</p><o:p /><p class="s">Two</p><p class="s">Three</p></div>`, 1},
		"one for each such element":          {`<div><o:p /><p>One</p></div><div><o:p /><p>Two</p></div>`, 2},
		"closed element around an element":   {prefixedPage, 1}, // the span inside fb:like
		"inside a nested unknown element":    {`<p>From <st1:place><st1:City><b>Paris</b></st1:City></st1:place></p>`, 1},
		"after a nested unknown element":     {`<p>From <st1:place><st1:City>Paris</st1:City><b>France</b></st1:place></p>`, 1},
		"word markup":                        {wordOnlyPage, 0},
		"text only":                          {`<p>Write to <editor@example.com> today</p><p>after</p>`, 0},
		"only elements hidden anywhere":      {`<p>Story</p><g:plusone size="tall" /><script>plus()</script><svg><circle r="1"/></svg><form><p>Search</p></form>`, 0},
		"empty":                              {`<p>One</p><o:p></o:p><p>Two</p>`, 0},
		"no unknown element":                 {`<article><h2>Story</h2><form><p>Search</p></form></article>`, 0},
	} {
		nodes, omitted, err := selectorTreeOmitted([]byte(c.source))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if omitted != c.want {
			t.Errorf("%s: omitted = %d, want %d", name, omitted, c.want)
		}
		// The count is beside the tree, never in it: the frame receives the
		// nodes, and they are what selectorTree alone returns.
		plain, err := selectorTree([]byte(c.source))
		if err != nil || !reflect.DeepEqual(plain, nodes) {
			t.Errorf("%s: counting changed the tree: %v", name, err)
		}
	}
	// A placeholder is not entered, so the walk's 100 levels do not bound
	// what is under it; the check reaches as deep as the parser allows.
	deep := strings.Repeat("<o:p>", 500)
	if _, omitted, err := selectorTreeOmitted([]byte(deep + "text")); err != nil || omitted != 0 {
		t.Fatalf("deeply nested unknown elements: omitted = %d, %v", omitted, err)
	}
	if _, omitted, err := selectorTreeOmitted([]byte(deep + "<p>found</p>")); err != nil || omitted != 1 {
		t.Fatalf("an element under deeply nested unknown elements: omitted = %d, %v", omitted, err)
	}
}

// The snapshot reply says how many placeholders hide elements, as a number
// and nothing else, and says nothing at all for a page without any, so the
// reply for every other page is what it was.
func TestSelectorSnapshotReportsOmittedContent(t *testing.T) {
	authn, err := auth.New("selector-test-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &fixtureFetcher{body: []byte(`<article><h2>Story</h2></article>`), pages: map[string][]byte{
		"https://example.com/unclosed": []byte(unclosedWidgetPage),
		"https://example.com/word":     []byte(wordOnlyPage),
	}}
	app := &App{Auth: authn, BaseURL: "http://localhost:8080", Scheduler: scheduler.New(nil, fetcher, 1, time.Second)}
	session, err := authn.Login("selector-test-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := httptest.NewRecorder()
	authn.Cookie(cookie, session)
	snapshot := func(address string) (map[string]json.RawMessage, string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/api/selector/snapshot", strings.NewReader(`{"url":"`+address+`","recipe":{"mode":"static"}}`))
		r.Header.Set("Origin", app.BaseURL)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", session.CSRF)
		r.AddCookie(cookie.Result().Cookies()[0])
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: snapshot = %d %s", address, w.Code, w.Body.String())
		}
		var reply map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
			t.Fatal(err)
		}
		return reply, w.Body.String()
	}
	keys := func(reply map[string]json.RawMessage) string {
		var names []string
		for k := range reply {
			names = append(names, k)
		}
		sort.Strings(names)
		return strings.Join(names, " ")
	}

	reply, body := snapshot("https://example.com/unclosed")
	if keys(reply) != "nodes omitted url" || string(reply["omitted"]) != "1" {
		t.Fatalf("unclosed element: keys %q, omitted %s", keys(reply), reply["omitted"])
	}
	for _, forbidden := range []string{"fb:like", "liked", "Swallowed", "article", "h2"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%q reached the browser", forbidden)
		}
	}
	for _, address := range []string{"https://example.com/word", "https://example.com/plain"} {
		if reply, _ := snapshot(address); keys(reply) != "nodes url" {
			t.Errorf("%s: keys %q, want only nodes and url", address, keys(reply))
		}
	}
	// A page without unknown elements gets the bytes it always got.
	nodes, err := selectorTree(fetcher.body)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(map[string]any{"nodes": nodes, "url": "https://example.com/plain"})
	if _, body := snapshot("https://example.com/plain"); body != string(want) {
		t.Fatalf("reply for a page without unknown elements changed: %s", body)
	}
}
