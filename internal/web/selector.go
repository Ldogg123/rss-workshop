package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"rss-workshop/internal/fetch"
	"rss-workshop/internal/model"
	"rss-workshop/internal/scheduler"
	"rss-workshop/internal/store"
)

// Only inert selector metadata and text cross into the management browser.
// IDs/classes describe the original DOM; they are never applied to the visible
// preview. Omitted subtrees keep their root in the mirror so sibling positions
// remain accurate. Attribute values belong only to the inert XML mirror, never
// to visible HTML. No source HTML, CSS, script, or active subtree is transmitted.
type selectorNode struct {
	Parent   int               `json:"parent"`
	Tag      string            `json:"tag,omitempty"`
	ID       string            `json:"id,omitempty"`
	Class    string            `json:"class,omitempty"`
	TestID   string            `json:"testid,omitempty"`
	Text     string            `json:"text,omitempty"`
	Alt      string            `json:"alt,omitempty"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Hidden   bool              `json:"hidden,omitempty"`
	Link     bool              `json:"link,omitempty"`
	Datetime bool              `json:"datetime,omitempty"`
}

var selectorTag = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var selectorAttr = regexp.MustCompile(`^[a-z_][a-z0-9_.-]*$`)

const maxSelectorAttrs = 64

func selectorAttribute(name, value string) bool {
	if len(name) > 128 || len(value) > 2048 || !selectorAttr.MatchString(name) {
		return false
	}
	if strings.HasPrefix(name, "on") || name == "style" || name == "srcdoc" {
		return false
	}
	data := strings.HasPrefix(name, "data-")
	if !data && !strings.HasPrefix(name, "aria-") && !strings.Contains(" id class title alt role href src srcset datetime lang dir rel target type name value width height loading decoding fetchpriority sizes media hreflang download cite longdesc poster itemprop itemscope itemtype itemid itemref content property about typeof resource vocab tabindex hidden open start reversed colspan rowspan scope headers abbr ", " "+name+" ") {
		return false
	}
	if data || strings.Contains(" href src srcset cite longdesc poster itemid itemtype resource vocab ", " "+name+" ") {
		// Browsers ignore ASCII whitespace/control characters in URL schemes.
		// Check each possible srcset candidate as well as a single URL. Even
		// these rejected strings could not execute in the XML mirror, but they
		// are not useful source-resource metadata for the selector.
		clean := strings.ToLower(strings.Map(func(r rune) rune {
			if r <= ' ' || r == 0x7f {
				return -1
			}
			return r
		}, value))
		for _, scheme := range []string{"javascript:", "vbscript:"} {
			if strings.HasPrefix(clean, scheme) || strings.Contains(clean, ","+scheme) {
				return false
			}
		}
	}
	return true
}

func selectorTree(body []byte) ([]selectorNode, error) {
	if len(body) > fetch.MaxBody {
		return nil, errors.New("page exceeds the 4 MiB snapshot limit")
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not parse page")
	}
	nodes := []selectorNode{}
	var walk func(*html.Node, int, int) error
	walk = func(n *html.Node, parent, depth int) error {
		if depth > 100 || len(nodes) >= 12000 {
			return errors.New("page is too complex for visual selection; use manual selectors (12,000 nodes / 100 levels maximum)")
		}
		if n.Type == html.TextNode {
			// Whitespace is significant for XPath text() and node() positions.
			nodes = append(nodes, selectorNode{Parent: parent, Text: n.Data})
			return nil
		}
		if n.Type == html.ElementNode {
			if !selectorTag.MatchString(n.Data) {
				return errors.New("page contains unsupported element names; use manual selectors")
			}
			v := selectorNode{Parent: parent, Tag: n.Data}
			v.Hidden = n.Namespace != "" || strings.Contains(" head script style iframe frame frameset object embed template noscript svg math canvas audio video source track link meta base form input textarea select option button ", " "+n.Data+" ")
			for _, a := range n.Attr {
				if v.Hidden || len(v.Attrs) >= maxSelectorAttrs {
					break
				}
				if a.Namespace != "" || len(a.Val) > 2048 || !selectorAttr.MatchString(a.Key) {
					continue
				}
				if _, exists := v.Attrs[a.Key]; exists {
					continue
				}
				switch a.Key {
				case "id":
					v.ID = a.Val
				case "class":
					v.Class = a.Val
				case "data-testid":
					v.TestID = a.Val
				case "alt":
					v.Alt = a.Val
				case "href":
					v.Link = true
				case "datetime":
					v.Datetime = true
				}
				if selectorAttribute(a.Key, a.Val) {
					if v.Attrs == nil {
						v.Attrs = make(map[string]string)
					}
					v.Attrs[a.Key] = a.Val
				}
			}
			parent = len(nodes)
			nodes = append(nodes, v)
			if v.Hidden {
				return nil
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := walk(c, parent, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(doc, -1, 0); err != nil {
		return nil, err
	}
	return nodes, nil
}

func (a *App) selectorSnapshot(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL    string       `json:"url"`
		Recipe model.Recipe `json:"recipe"`
	}
	if err := decode(w, r, &in); err != nil {
		failure(w, 400, err)
		return
	}
	if len(in.URL) > 4096 {
		http.Error(w, "source URL is too long", 400)
		return
	}
	result, err := a.Scheduler.Snapshot(r.Context(), in.URL, in.Recipe)
	if err != nil {
		status := 422
		if errors.Is(err, scheduler.ErrBusy) {
			status = 503
		}
		failure(w, status, err)
		return
	}
	if result.Status != 200 {
		http.Error(w, "source did not return a complete page", 422)
		return
	}
	nodes, err := selectorTree(result.Body)
	if err != nil {
		failure(w, 422, err)
		return
	}
	data, err := json.Marshal(map[string]any{"nodes": nodes, "url": result.URL})
	if err != nil || len(data) > 4<<20 {
		http.Error(w, "sanitized snapshot exceeds 4 MiB; use manual selectors", 422)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// This public shell contains no user data. The authenticated parent sends a
// bounded snapshot after verifying the iframe's WindowProxy and opaque origin.
// A separate response policy avoids inheriting or loosening the app script CSP.
func (a *App) selectorFrame(w http.ResponseWriter, r *http.Request) {
	nonce := store.ID()
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	js, _ := files.ReadFile("assets/selector-frame.js")
	css, _ := files.ReadFile("assets/selector-frame.css")
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Page selection preview</title><style nonce="%s">%s</style></head><body><main id="page"></main><script nonce="%s">%s</script></body></html>`, nonce, css, nonce, js)
}
