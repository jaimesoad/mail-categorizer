package rules

import (
	"context"
	"testing"

	"github.com/jaimesoad/mail-categorizer/internal/features"
)

func set() *features.Set {
	return features.NewSet(
		features.Feature{Name: "sender.domain", Value: "MyBank.example", Confidence: 1},
		features.Feature{Name: "content.intent", Value: "transaction", Confidence: 0.9},
		features.Feature{Name: "content.intent", Value: "notification", Confidence: 0.5},
		features.Feature{Name: "attachment.count", Value: "2", Confidence: 1},
		features.Feature{Name: "subject.tokens", Value: "payment", Confidence: 0.8},
	)
}

func match(t *testing.T, c Condition) bool {
	t.Helper()
	m, err := Compile(c)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return m.Match(set())
}

func lf(f, op string, v any) Condition { return Condition{Feature: f, Op: op, Value: v} }

func TestOperators(t *testing.T) {
	cases := []struct {
		name string
		c    Condition
		want bool
	}{
		{"equals case-insens", lf("sender.domain", "equals", "mybank.example"), true},
		{"equals default op", Condition{Feature: "sender.domain", Value: "mybank.example"}, true},
		{"equals miss", lf("sender.domain", "equals", "other.com"), false},
		{"not_equals", lf("sender.domain", "not_equals", "other.com"), true},
		{"not_equals multi-value", lf("content.intent", "not_equals", "notification"), false},
		{"not_equals missing feature", lf("nope", "not_equals", "x"), false},
		{"contains", lf("sender.domain", "contains", "bank"), true},
		{"starts_with", lf("sender.domain", "starts_with", "mybank"), true},
		{"ends_with", lf("sender.domain", "ends_with", ".example"), true},
		{"regex", lf("sender.domain", "regex", `^my.*\.example$`), true},
		{"exists", Condition{Feature: "attachment.count", Op: "exists"}, true},
		{"exists missing", Condition{Feature: "nope", Op: "exists"}, false},
		{"gt", lf("attachment.count", "gt", 1), true},
		{"gte", lf("attachment.count", "gte", 2), true},
		{"lt", lf("attachment.count", "lt", 2), false},
		{"lte", lf("attachment.count", "lte", 2), true},
		{"in", lf("content.intent", "in", []any{"receipt", "transaction"}), true},
		{"in miss", lf("content.intent", "in", []any{"receipt"}), false},
		{"min_confidence filters", Condition{Feature: "content.intent", Op: "equals", Value: "notification", MinConfidence: 0.8}, false},
	}
	for _, tc := range cases {
		if got := match(t, tc.c); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestBooleanCombinators(t *testing.T) {
	yes, no := lf("sender.domain", "equals", "mybank.example"), lf("sender.domain", "equals", "x")
	if !match(t, Condition{All: []Condition{yes, lf("content.intent", "equals", "transaction")}}) {
		t.Error("AND")
	}
	if match(t, Condition{All: []Condition{yes, no}}) {
		t.Error("AND with false")
	}
	if !match(t, Condition{Any: []Condition{no, yes}}) {
		t.Error("OR")
	}
	if !match(t, Condition{Not: &no}) {
		t.Error("NOT")
	}
	if match(t, Condition{Not: &yes}) {
		t.Error("NOT true")
	}
}

func TestCompileErrors(t *testing.T) {
	bad := []Condition{
		{},
		{Feature: "a", Op: "bogus", Value: "x"},
		{Feature: "a", Op: "regex", Value: "("},
		{Feature: "a", Op: "gt", Value: "abc"},
		{Feature: "a", Op: "in", Value: "notalist"},
		{Feature: "a", Value: "x", All: []Condition{{Feature: "b", Value: "y"}}},
	}
	for i, c := range bad {
		if _, err := Compile(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestEngineEmitsWeightedEvidence(t *testing.T) {
	e, err := NewEngine([]Rule{
		{ID: "bank", Category: "finance.bank", Weight: 0.7, Reason: "dominio bancario", When: lf("sender.domain", "equals", "mybank.example")},
		{ID: "neg", Category: "work", Weight: -0.5, When: lf("content.intent", "equals", "transaction")},
		{ID: "miss", Category: "work", Weight: 0.5, When: lf("sender.domain", "equals", "zzz")},
		{ID: "off", Category: "work", Weight: 0.5, Disabled: true, When: lf("sender.domain", "equals", "mybank.example")},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := e.Evidence(context.Background(), emailZero, set())
	if len(ev) != 2 || ev[0].Ref != "bank" || ev[1].Weight != -0.5 || ev[0].Reason != "dominio bancario" {
		t.Fatalf("got %+v", ev)
	}
}

func TestEngineValidation(t *testing.T) {
	ok := lf("a", "equals", "b")
	for name, rs := range map[string][]Rule{
		"dup id":      {{ID: "x", Category: "c", Weight: 0.5, When: ok}, {ID: "x", Category: "c", Weight: 0.5, When: ok}},
		"zero weight": {{ID: "x", Category: "c", Weight: 0, When: ok}},
		"big weight":  {{ID: "x", Category: "c", Weight: 1.5, When: ok}},
		"no category": {{ID: "x", Weight: 0.5, When: ok}},
		"no id":       {{Category: "c", Weight: 0.5, When: ok}},
	} {
		if _, err := NewEngine(rs); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
