// Package categories models the user-defined, hierarchical category tree.
package categories

import (
	"fmt"
	"sort"
	"strings"
)

// UnknownID is the reserved category meaning "not enough evidence" (§32).
const UnknownID = "unknown"

// Category is a node of the tree.
type Category struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
}

// Tree is an immutable view over a validated set of categories.
type Tree struct {
	byID     map[string]Category
	children map[string][]string // "" = roots; sorted by id
}

// NewTree validates cats (unique ids, existing parents, no cycles) and builds a
// Tree. The reserved Unknown category is always added as a root.
func NewTree(cats []Category) (*Tree, error) {
	t := &Tree{byID: map[string]Category{}, children: map[string][]string{}}
	for _, c := range cats {
		if c.ID == "" {
			return nil, fmt.Errorf("category with empty id")
		}
		if c.ID == UnknownID && c.ParentID != "" {
			return nil, fmt.Errorf("category %q is reserved and cannot have a parent", UnknownID)
		}
		if _, dup := t.byID[c.ID]; dup {
			return nil, fmt.Errorf("duplicate category id %q", c.ID)
		}
		if c.Name == "" {
			c.Name = c.ID
		}
		t.byID[c.ID] = c
	}
	if _, ok := t.byID[UnknownID]; !ok {
		t.byID[UnknownID] = Category{ID: UnknownID, Name: "Desconocido", Description: "Sin evidencia suficiente para clasificar", Enabled: true}
	}
	for id, c := range t.byID {
		if c.ParentID != "" {
			if _, ok := t.byID[c.ParentID]; !ok {
				return nil, fmt.Errorf("category %q: parent %q does not exist", id, c.ParentID)
			}
		}
		// cycle check
		seen := map[string]bool{id: true}
		for p := c.ParentID; p != ""; p = t.byID[p].ParentID {
			if seen[p] {
				return nil, fmt.Errorf("category %q: cycle in hierarchy", id)
			}
			seen[p] = true
		}
		t.children[c.ParentID] = append(t.children[c.ParentID], id)
	}
	for k := range t.children {
		sort.Strings(t.children[k])
	}
	return t, nil
}

// Get returns a category by id.
func (t *Tree) Get(id string) (Category, bool) { c, ok := t.byID[id]; return c, ok }

// Has reports whether id exists.
func (t *Tree) Has(id string) bool { _, ok := t.byID[id]; return ok }

// Enabled reports whether id exists and it and all its ancestors are enabled.
func (t *Tree) Enabled(id string) bool {
	for cur := id; cur != ""; cur = t.byID[cur].ParentID {
		c, ok := t.byID[cur]
		if !ok || !c.Enabled {
			return false
		}
	}
	return true
}

// Children returns the enabled children of id ("" for roots), sorted by id.
// Unknown is never returned: it is handled as residual mass by the classifier.
func (t *Tree) Children(id string) []string {
	var out []string
	for _, c := range t.children[id] {
		if c == UnknownID || !t.byID[c].Enabled {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Roots returns the enabled top-level categories (excluding Unknown).
func (t *Tree) Roots() []string { return t.Children("") }

// Ancestors returns the ids from the parent of id up to the root.
func (t *Tree) Ancestors(id string) []string {
	var out []string
	for p := t.byID[id].ParentID; p != ""; p = t.byID[p].ParentID {
		out = append(out, p)
	}
	return out
}

// IsAncestor reports whether anc is a strict ancestor of id.
func (t *Tree) IsAncestor(anc, id string) bool {
	for _, a := range t.Ancestors(id) {
		if a == anc {
			return true
		}
	}
	return false
}

// Path returns the human readable path ("Finance / Bank").
func (t *Tree) Path(id string) string {
	c, ok := t.byID[id]
	if !ok {
		return id
	}
	parts := []string{c.Name}
	for _, a := range t.Ancestors(id) {
		parts = append([]string{t.byID[a].Name}, parts...)
	}
	return strings.Join(parts, " / ")
}

// All returns every category sorted by id.
func (t *Tree) All() []Category {
	out := make([]Category, 0, len(t.byID))
	for _, c := range t.byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Walk visits categories depth-first in sorted order with their depth.
func (t *Tree) Walk(fn func(c Category, depth int)) {
	var rec func(parent string, depth int)
	rec = func(parent string, depth int) {
		for _, id := range t.children[parent] {
			fn(t.byID[id], depth)
			rec(id, depth+1)
		}
	}
	rec("", 0)
}
