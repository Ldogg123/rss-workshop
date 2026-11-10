package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"regexp"
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
