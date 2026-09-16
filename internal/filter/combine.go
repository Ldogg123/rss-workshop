package filter

import "rss-workshop/internal/model"

// Combine merges a feed's own rules with the library filters it uses into the
// single set that previews, refreshes, history pruning and exports evaluate.
// A story must pass every include side and must match no exclude side, so
// includes join under "all" and excludes under "any". Children of a root that
// already uses the joining operator are spliced in rather than nested, which
// keeps the merged tree as shallow as its deepest part. The result is not
// validated here: Compile applies the usual bounds to the whole merged set, so
// a combination that is too large is rejected like an oversized feed filter.
func Combine(own *model.FilterSet, shared []model.FilterSet) *model.FilterSet {
	if len(shared) == 0 {
		return own
	}
	var include, exclude []*model.FilterRule
	if own != nil {
		include, exclude = appendRule(include, own.Include), appendRule(exclude, own.Exclude)
	}
	for i := range shared {
		include, exclude = appendRule(include, shared[i].Include), appendRule(exclude, shared[i].Exclude)
	}
	out := &model.FilterSet{Include: join("all", include), Exclude: join("any", exclude)}
	if out.Include == nil && out.Exclude == nil {
		return nil
	}
	return out
}

func appendRule(rules []*model.FilterRule, rule *model.FilterRule) []*model.FilterRule {
	if rule == nil {
		return rules
	}
	return append(rules, rule)
}

// join copies every node it builds or splices, so the merged set never aliases
// the rule slices of the stored filters it came from.
func join(op string, parts []*model.FilterRule) *model.FilterRule {
	switch len(parts) {
	case 0:
		return nil
	case 1:
		copied := copyRule(*parts[0])
		return &copied
	}
	group := model.FilterRule{Op: op}
	for _, part := range parts {
		if part.Op == op {
			for _, child := range part.Rules {
				group.Rules = append(group.Rules, copyRule(child))
			}
		} else {
			group.Rules = append(group.Rules, copyRule(*part))
		}
	}
	return &group
}

func copyRule(r model.FilterRule) model.FilterRule {
	out := model.FilterRule{Op: r.Op, Field: r.Field}
	if r.Keywords != nil {
		out.Keywords = append([]string(nil), r.Keywords...)
	}
	for _, child := range r.Rules {
		out.Rules = append(out.Rules, copyRule(child))
	}
	return out
}
