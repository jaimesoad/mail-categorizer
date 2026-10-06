package categories

import "testing"

func cats() []Category {
	return []Category{
		{ID: "work", Name: "Work", Enabled: true},
		{ID: "finance", Name: "Finance", Enabled: true},
		{ID: "finance.bank", ParentID: "finance", Name: "Bank", Enabled: true},
		{ID: "finance.old", ParentID: "finance", Name: "Old", Enabled: false},
	}
}

func TestTreeNavigation(t *testing.T) {
	tr, err := NewTree(cats())
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Has(UnknownID) {
		t.Error("Unknown must always exist")
	}
	if got := tr.Roots(); len(got) != 2 || got[0] != "finance" || got[1] != "work" {
		t.Errorf("roots (unknown excluded, sorted): %v", got)
	}
	if got := tr.Children("finance"); len(got) != 1 || got[0] != "finance.bank" {
		t.Errorf("disabled children must be hidden: %v", got)
	}
	if tr.Path("finance.bank") != "Finance / Bank" {
		t.Errorf("path: %s", tr.Path("finance.bank"))
	}
	if !tr.IsAncestor("finance", "finance.bank") || tr.IsAncestor("finance.bank", "finance") || tr.IsAncestor("work", "finance.bank") {
		t.Error("IsAncestor")
	}
	if tr.Enabled("finance.old") || !tr.Enabled("finance.bank") || tr.Enabled("nope") {
		t.Error("Enabled")
	}
	var order []string
	tr.Walk(func(c Category, d int) { order = append(order, c.ID) })
	if len(order) < 4 || order[0] != "finance" || order[1] != "finance.bank" {
		t.Errorf("walk (depth first): %v", order)
	}
}

func TestDisabledParentDisablesSubtree(t *testing.T) {
	c := cats()
	c[1].Enabled = false
	tr, _ := NewTree(c)
	if tr.Enabled("finance.bank") {
		t.Error("children of a disabled category are disabled")
	}
}

func TestTreeValidation(t *testing.T) {
	bad := map[string][]Category{
		"empty id":  {{ID: ""}},
		"duplicate": {{ID: "a"}, {ID: "a"}},
		"parent":    {{ID: "a", ParentID: "zzz"}},
		"cycle":     {{ID: "a", ParentID: "b"}, {ID: "b", ParentID: "a"}},
		"self":      {{ID: "a", ParentID: "a"}},
		"unknown":   {{ID: "unknown", ParentID: "a"}, {ID: "a"}},
	}
	for name, c := range bad {
		if _, err := NewTree(c); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
