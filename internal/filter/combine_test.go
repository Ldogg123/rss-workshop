package filter

import (
	"reflect"
	"strings"
	"testing"

	"rss-workshop/internal/model"
)

func TestCombineJoinsIncludesAndExcludes(t *testing.T) {
	own := &model.FilterSet{Include: &model.FilterRule{Op: "all", Rules: []model.FilterRule{leaf("contains_any", "title", "linux")}}}
	shared := []model.FilterSet{
		{Include: ptr(leaf("contains_any", "description", "docker")), Exclude: ptr(leaf("contains_any", "link", "/sponsored/"))},
		{Exclude: ptr(group("any", leaf("contains_any", "title", "advert"), leaf("contains_any", "title", "promo")))},
	}
	got := Combine(own, shared)
	want := &model.FilterSet{
		Include: &model.FilterRule{Op: "all", Rules: []model.FilterRule{leaf("contains_any", "title", "linux"), leaf("contains_any", "description", "docker")}},
		Exclude: &model.FilterRule{Op: "any", Rules: []model.FilterRule{leaf("contains_any", "link", "/sponsored/"), leaf("contains_any", "title", "advert"), leaf("contains_any", "title", "promo")}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("combined set:\n got %+v\nwant %+v", got, want)
	}
	m := mustCompile(t, got)
	for _, tc := range []struct {
		item model.Item
		want bool
	}{
		{model.Item{Title: "Linux", HTML: "Docker", URL: "https://example.com/a"}, true},
		{model.Item{Title: "Linux", HTML: "Podman", URL: "https://example.com/a"}, false},
		{model.Item{Title: "Linux promo", HTML: "Docker", URL: "https://example.com/a"}, false},
		{model.Item{Title: "Linux", HTML: "Docker", URL: "https://example.com/sponsored/a"}, false},
	} {
		if m.Match(tc.item) != tc.want {
			t.Fatalf("match %+v: want %v", tc.item, tc.want)
		}
	}
	// The merged tree must not share slices with the stored rules it came from.
	got.Include.Rules[0].Keywords[0] = "changed"
	got.Exclude.Rules[1].Keywords[0] = "changed"
	if own.Include.Rules[0].Keywords[0] != "linux" || shared[1].Exclude.Rules[0].Keywords[0] != "advert" {
		t.Fatal("combined set aliases its sources")
	}
}

func TestCombineWithoutLibraryFiltersKeepsOwnRules(t *testing.T) {
	own := &model.FilterSet{Exclude: ptr(leaf("contains_any", "title", "x"))}
	if Combine(own, nil) != own || Combine(nil, nil) != nil {
		t.Fatal("feed without library filters must evaluate its own rules unchanged")
	}
	if got := Combine(nil, []model.FilterSet{{Include: ptr(leaf("contains_any", "title", "y"))}}); got.Exclude != nil || got.Include.Keywords[0] != "y" {
		t.Fatalf("single library filter: %+v", got)
	}
}

func TestCombinedBoundsApplyToWholeSet(t *testing.T) {
	half := make([]string, MaxKeywords/2+1)
	for i := range half {
		half[i] = strings.Repeat("k", i%5+1) + string(rune('a'+i%26))
	}
	shared := []model.FilterSet{{Include: ptr(leaf("contains_any", "title", half...))}, {Exclude: ptr(leaf("contains_any", "title", half...))}}
	if _, err := Compile(Combine(nil, shared[:1])); err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(Combine(nil, shared)); err == nil || !strings.Contains(err.Error(), "keywords") {
		t.Fatalf("combined keyword budget not enforced: %v", err)
	}
	// Splicing same-operator roots keeps a maximally nested part within depth.
	deep := leaf("contains_any", "title", "deep")
	for i := 1; i < MaxDepth; i++ {
		deep = group("any", deep)
	}
	combined := Combine(&model.FilterSet{Include: ptr(group("all", leaf("contains_any", "title", "a")))}, []model.FilterSet{{Include: ptr(group("all", deep.Rules[0]))}})
	if _, err := Compile(combined); err != nil {
		t.Fatalf("spliced groups gained depth: %v", err)
	}
}

func ptr(r model.FilterRule) *model.FilterRule { return &r }
