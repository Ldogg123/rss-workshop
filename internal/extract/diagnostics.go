package extract

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"rss-workshop/internal/diagnostics"
	"rss-workshop/internal/model"
)

// receivedSample describes only the selected field value, never its surrounding
// source markup. URL credentials and queries are not useful for diagnosing a
// field type mismatch, and must not become retained log data.
func receivedSample(raw string) string {
	raw = strings.TrimSpace(raw)
	kind := ""
	if u, err := url.Parse(raw); err == nil {
		if u.IsAbs() || strings.HasPrefix(raw, "//") {
			kind = "URL "
			if strings.HasPrefix(raw, "//") {
				raw = "[URL omitted]"
			}
		} else if u.RawQuery != "" && !strings.ContainsAny(raw, " \t\r\n") {
			u.RawQuery, u.Fragment, u.ForceQuery = "", "", false
			raw = u.String()
		}
	}
	raw = diagnostics.SafeText(raw, max(len(raw), 512))
	raw = strings.Join(strings.Fields(raw), " ")
	runes := []rune(raw)
	if len(runes) > 120 || len(raw) > 320 {
		runes = runes[:min(len(runes), 119)]
		for len(string(runes)) > 317 {
			runes = runes[:len(runes)-1]
		}
		raw = string(runes) + "…"
	}
	return kind + strconv.Quote(raw)
}

func fieldMismatch(name, expected, raw string, node *html.Node, f model.Field, defaultAttr string) string {
	received := "nothing"
	switch {
	case strings.TrimSpace(raw) != "":
		received = receivedSample(raw)
	case node == nil:
		received += " (selector matched no nodes)"
	default:
		attribute := f.Attr
		if attribute == "" {
			attribute = defaultAttr
		}
		if attribute == "" || isAttribute(node) {
			received += " (selected value is empty)"
		} else {
			present := false
			for _, a := range node.Attr {
				if a.Key == attribute {
					present = true
					break
				}
			}
			if present {
				received += fmt.Sprintf(" (selected element's %s attribute is empty)", receivedSample(attribute))
			} else {
				received += fmt.Sprintf(" (selected element has no %s attribute)", receivedSample(attribute))
			}
		}
	}
	return fmt.Sprintf("%s expected %s but received %s", name, expected, received)
}
