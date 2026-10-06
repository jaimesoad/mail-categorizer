// Package rules implements the deterministic rule engine (§8): conditions over
// features combined with AND / OR / NOT that emit weighted evidence.
package rules

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jaimesoad/mail-categorizer/internal/features"
)

// Operators supported by a leaf condition.
const (
	OpEquals     = "equals"
	OpNotEquals  = "not_equals"
	OpContains   = "contains"
	OpStartsWith = "starts_with"
	OpEndsWith   = "ends_with"
	OpRegex      = "regex"
	OpExists     = "exists"
	OpGT         = "gt"
	OpGTE        = "gte"
	OpLT         = "lt"
	OpLTE        = "lte"
	OpIn         = "in"
)

// Condition is a node of the condition tree. Exactly one of All, Any, Not or
// Feature must be set. It is the YAML/JSON representation; Compile turns it into
// a Matcher.
type Condition struct {
	All []Condition `yaml:"all,omitempty" json:"all,omitempty"`
	Any []Condition `yaml:"any,omitempty" json:"any,omitempty"`
	Not *Condition  `yaml:"not,omitempty" json:"not,omitempty"`

	Feature string `yaml:"feature,omitempty" json:"feature,omitempty"`
	Op      string `yaml:"op,omitempty" json:"op,omitempty"`
	Value   any    `yaml:"value,omitempty" json:"value,omitempty"`
	// MinConfidence ignores feature values whose confidence is lower.
	MinConfidence float64 `yaml:"min_confidence,omitempty" json:"min_confidence,omitempty"`
}

// Matcher evaluates a compiled condition against a feature set.
type Matcher interface {
	Match(s *features.Set) bool
}

// Compile validates and compiles a condition tree.
func Compile(c Condition) (Matcher, error) {
	n := 0
	if len(c.All) > 0 {
		n++
	}
	if len(c.Any) > 0 {
		n++
	}
	if c.Not != nil {
		n++
	}
	if c.Feature != "" {
		n++
	}
	if n != 1 {
		return nil, fmt.Errorf("condition must set exactly one of all/any/not/feature (got %d)", n)
	}
	switch {
	case len(c.All) > 0:
		ms, err := compileAll(c.All)
		return andM(ms), err
	case len(c.Any) > 0:
		ms, err := compileAll(c.Any)
		return orM(ms), err
	case c.Not != nil:
		m, err := Compile(*c.Not)
		return notM{m}, err
	}
	return compileLeaf(c)
}

func compileAll(cs []Condition) ([]Matcher, error) {
	ms := make([]Matcher, 0, len(cs))
	for i, c := range cs {
		m, err := Compile(c)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		ms = append(ms, m)
	}
	return ms, nil
}

type andM []Matcher
type orM []Matcher
type notM struct{ m Matcher }

func (a andM) Match(s *features.Set) bool {
	for _, m := range a {
		if !m.Match(s) {
			return false
		}
	}
	return true
}
func (o orM) Match(s *features.Set) bool {
	for _, m := range o {
		if m.Match(s) {
			return true
		}
	}
	return false
}
func (n notM) Match(s *features.Set) bool { return !n.m.Match(s) }

type leaf struct {
	feature string
	op      string
	str     string
	num     float64
	set     map[string]struct{}
	re      *regexp.Regexp
	minConf float64
}

func compileLeaf(c Condition) (Matcher, error) {
	l := &leaf{feature: c.Feature, op: c.Op, minConf: c.MinConfidence}
	if l.op == "" {
		l.op = OpEquals
	}
	switch l.op {
	case OpExists:
	case OpEquals, OpNotEquals, OpContains, OpStartsWith, OpEndsWith:
		if c.Value == nil {
			return nil, fmt.Errorf("%s %s: value required", c.Feature, l.op)
		}
		l.str = strings.ToLower(scalar(c.Value))
	case OpRegex:
		re, err := regexp.Compile("(?i)" + scalar(c.Value))
		if err != nil {
			return nil, fmt.Errorf("%s regex: %w", c.Feature, err)
		}
		l.re = re
	case OpGT, OpGTE, OpLT, OpLTE:
		f, err := strconv.ParseFloat(scalar(c.Value), 64)
		if err != nil {
			return nil, fmt.Errorf("%s %s: numeric value required", c.Feature, l.op)
		}
		l.num = f
	case OpIn:
		vs, ok := c.Value.([]any)
		if !ok || len(vs) == 0 {
			return nil, fmt.Errorf("%s in: non-empty list required", c.Feature)
		}
		l.set = map[string]struct{}{}
		for _, v := range vs {
			l.set[strings.ToLower(scalar(v))] = struct{}{}
		}
	default:
		return nil, fmt.Errorf("unknown operator %q", c.Op)
	}
	return l, nil
}

func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

// Match: a feature may have several values; a leaf matches when any value
// satisfies it, except not_equals which requires that none equals the operand.
func (l *leaf) Match(s *features.Set) bool {
	var vals []features.Feature
	for _, f := range s.Values(l.feature) {
		if f.Confidence >= l.minConf {
			vals = append(vals, f)
		}
	}
	switch l.op {
	case OpExists:
		return len(vals) > 0
	case OpNotEquals:
		for _, f := range vals {
			if strings.ToLower(f.Value) == l.str {
				return false
			}
		}
		return len(vals) > 0
	}
	for _, f := range vals {
		v := strings.ToLower(f.Value)
		ok := false
		switch l.op {
		case OpEquals:
			ok = v == l.str
		case OpContains:
			ok = strings.Contains(v, l.str)
		case OpStartsWith:
			ok = strings.HasPrefix(v, l.str)
		case OpEndsWith:
			ok = strings.HasSuffix(v, l.str)
		case OpRegex:
			ok = l.re.MatchString(f.Value)
		case OpIn:
			_, ok = l.set[v]
		case OpGT, OpGTE, OpLT, OpLTE:
			n, err := strconv.ParseFloat(f.Value, 64)
			if err != nil {
				continue
			}
			switch l.op {
			case OpGT:
				ok = n > l.num
			case OpGTE:
				ok = n >= l.num
			case OpLT:
				ok = n < l.num
			case OpLTE:
				ok = n <= l.num
			}
		}
		if ok {
			return true
		}
	}
	return false
}
