package model

// FilterSet includes an item when its include rules match and its exclude rules
// do not. Either side is optional; an empty set accepts every valid item.
type FilterSet struct {
	Include *FilterRule `json:"include,omitempty"`
	Exclude *FilterRule `json:"exclude,omitempty"`
}

// FilterRule is either an all/any group with child rules, or a contains_any /
// contains_all leaf with one field and a list of literal keywords.
type FilterRule struct {
	Op       string       `json:"op"`
	Rules    []FilterRule `json:"rules,omitempty"`
	Field    string       `json:"field,omitempty"`
	Keywords []string     `json:"keywords,omitempty"`
}

type FilterExample struct {
	Title  string `json:"title"`
	Reason string `json:"reason"`
}
