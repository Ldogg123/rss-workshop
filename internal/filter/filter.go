// Package filter evaluates bounded literal keyword rules on extracted items.
package filter

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"rss-workshop/internal/model"
)

const (
	MaxKeywords          = 500
	MaxNodes             = 128
	MaxDepth             = 8
	MaxKeywordCharacters = 256
)

type compiledRule struct {
	op       string
	field    int
	keywords []string
	rules    []*compiledRule
}

// Matcher owns a detached, immutable copy of the compiled rules and can be
// shared between goroutines. All item-specific work stays local to each call.
type Matcher struct {
	include, exclude *compiledRule
}

type Decision struct {
	Matched bool
	Reason  string
}

type budget struct{ nodes, keywords int }

// Compile validates the complete tree before it can be saved or evaluated.
// Its limits apply across include and exclude together.
func Compile(set *model.FilterSet) (*Matcher, error) {
	m := &Matcher{}
	if set == nil {
		return m, nil
	}
	b := &budget{}
	var err error
	if set.Include != nil {
		m.include, err = compileRule(set.Include, b, 1, "include")
		if err != nil {
			return nil, err
		}
	}
	if set.Exclude != nil {
		m.exclude, err = compileRule(set.Exclude, b, 1, "exclude")
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func compileRule(r *model.FilterRule, b *budget, depth int, path string) (*compiledRule, error) {
	if depth > MaxDepth {
		return nil, fmt.Errorf("filters exceed %d levels of nesting", MaxDepth)
	}
	b.nodes++
	if b.nodes > MaxNodes {
		return nil, fmt.Errorf("filters exceed %d rule nodes", MaxNodes)
	}
	c := &compiledRule{op: r.Op}
	switch r.Op {
	case "all", "any":
		if len(r.Rules) == 0 || r.Field != "" || r.Keywords != nil {
			return nil, fmt.Errorf("%s filter group requires child rules and cannot contain a field or keywords", path)
		}
		for i := range r.Rules {
			child, err := compileRule(&r.Rules[i], b, depth+1, fmt.Sprintf("%s rule %d", path, i+1))
			if err != nil {
				return nil, err
			}
			c.rules = append(c.rules, child)
		}
	case "contains_any", "contains_all":
		if r.Rules != nil || len(r.Keywords) == 0 {
			return nil, fmt.Errorf("%s keyword rule requires keywords and cannot contain child rules", path)
		}
		switch r.Field {
		case "title":
			c.field = 0
		case "description":
			c.field = 1
		case "link":
			c.field = 2
		default:
			return nil, fmt.Errorf("%s filter field must be title, description, or link", path)
		}
		if len(r.Keywords) > MaxKeywords-b.keywords {
			return nil, fmt.Errorf("filters exceed %d keywords", MaxKeywords)
		}
		b.keywords += len(r.Keywords)
		for i, keyword := range r.Keywords {
			keyword = strings.TrimSpace(keyword)
			if keyword == "" || !utf8.ValidString(keyword) || utf8.RuneCountInString(keyword) > MaxKeywordCharacters {
				return nil, fmt.Errorf("%s keyword %d must contain 1–%d Unicode characters", path, i+1, MaxKeywordCharacters)
			}
			c.keywords = append(c.keywords, normalize(keyword))
		}
	default:
		return nil, fmt.Errorf("%s filter operator must be all, any, contains_any, or contains_all", path)
	}
	return c, nil
}

func (m *Matcher) Match(item model.Item) bool { return m.evaluate(item, false).Matched }

// Evaluate adds a short explanation without copying source text, URLs, or
// keyword lists into diagnostics. Match avoids constructing these strings.
func (m *Matcher) Evaluate(item model.Item) Decision { return m.evaluate(item, true) }

func (m *Matcher) evaluate(item model.Item, explain bool) Decision {
	if m == nil {
		return Decision{Matched: true}
	}
	s := subject{item: item}
	if m.include != nil && !m.include.match(&s) {
		if explain {
			return Decision{Reason: m.include.reason(false)}
		}
		return Decision{}
	}
	if m.exclude != nil && m.exclude.match(&s) {
		if explain {
			return Decision{Reason: m.exclude.reason(true)}
		}
		return Decision{}
	}
	return Decision{Matched: true}
}

func (r *compiledRule) match(s *subject) bool {
	switch r.op {
	case "all":
		for _, child := range r.rules {
			if !child.match(s) {
				return false
			}
		}
		return true
	case "any":
		for _, child := range r.rules {
			if child.match(s) {
				return true
			}
		}
		return false
	case "contains_all":
		value := s.value(r.field)
		for _, keyword := range r.keywords {
			if !strings.Contains(value, keyword) {
				return false
			}
		}
		return true
	default:
		value := s.value(r.field)
		for _, keyword := range r.keywords {
			if strings.Contains(value, keyword) {
				return true
			}
		}
		return false
	}
}

func (r *compiledRule) reason(excluded bool) string {
	if r.op == "all" || r.op == "any" {
		if excluded {
			return "Matched the exclude rules."
		}
		return "Did not match the include rules."
	}
	field := []string{"Title", "Description", "Link"}[r.field]
	quantifier := "any"
	if r.op == "contains_all" {
		quantifier = "all"
	}
	if excluded {
		if quantifier == "any" {
			quantifier = "at least one of"
		}
		return fmt.Sprintf("%s matched %s the exclude keywords.", field, quantifier)
	}
	return fmt.Sprintf("%s did not match %s of the include keywords.", field, quantifier)
}
