// Package schema implements declarative structured extraction — a
// YAML or JSON file describes a nested shape of selectors, and
// Extract walks an HTML document to produce a map matching that
// shape. It's the v1 answer to "Firecrawl schema extract" without
// any LLM in the loop.
//
// v1 scope:
//   - selector + optional attr → string
//   - multiple: true → array of strings, or array of objects if
//     nested fields are declared
//   - nested fields under `fields:` → object, scoped to the match
//   - empty selector inside a nested context = "self" (the iterated
//     parent element) — required for the common "array of objects
//     where each object needs the link's text and href" case
//   - missing matches → field omitted from output
//
// v2 additions:
//   - fallback selectors: selector accepts a list of strings; first
//     match wins
//   - transforms: post-extraction pipeline on leaf string values
//     (trim, regex, lowercase, uppercase, split)
//
// The `version` field in the schema is required. Version 1 and 2
// are both supported. Version 1 schemas reject v2-only features
// (multi-selector, transforms) at validation time.
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"gopkg.in/yaml.v3"
)

// Schema is the top-level structured-extraction spec.
type Schema struct {
	Version int                `yaml:"version" json:"version"`
	Fields  map[string]*Field  `yaml:"fields" json:"fields"`
}

// Field is one extraction rule. An empty Selector is only valid
// inside a nested Fields block, where it means "the current iterated
// parent element" (self).
type Field struct {
	Selector   SelectorSpec       `yaml:"selector" json:"selector"`
	Attr       string             `yaml:"attr,omitempty" json:"attr,omitempty"`
	Multiple   bool               `yaml:"multiple,omitempty" json:"multiple,omitempty"`
	Fields     map[string]*Field  `yaml:"fields,omitempty" json:"fields,omitempty"`
	Transforms []Transform        `yaml:"transforms,omitempty" json:"transforms,omitempty"`
}

// SelectorSpec is a CSS selector that accepts either a single string
// or a list of fallback selectors. First match wins during extraction.
type SelectorSpec []string

// UnmarshalYAML handles both `selector: "h1"` and `selector: ["h1", "h2"]`.
func (s *SelectorSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = SelectorSpec{value.Value}
		return nil
	case yaml.SequenceNode:
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*s = SelectorSpec(list)
		return nil
	default:
		return fmt.Errorf("selector must be a string or list of strings, got %v", value.Kind)
	}
}

// UnmarshalJSON handles both `"selector": "h1"` and `"selector": ["h1", "h2"]`.
func (s *SelectorSpec) UnmarshalJSON(data []byte) error {
	// Try string first.
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*s = SelectorSpec{single}
		return nil
	}
	// Try list.
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("selector must be a string or list of strings")
	}
	*s = SelectorSpec(list)
	return nil
}

// MarshalYAML emits a bare string when the spec has exactly one entry,
// a list otherwise. This preserves round-trip fidelity for v1 schemas.
func (s SelectorSpec) MarshalYAML() (any, error) {
	if len(s) == 1 {
		return s[0], nil
	}
	return []string(s), nil
}

// MarshalJSON emits a bare string when the spec has exactly one entry.
func (s SelectorSpec) MarshalJSON() ([]byte, error) {
	if len(s) == 1 {
		return json.Marshal(s[0])
	}
	return json.Marshal([]string(s))
}

// IsEmpty returns true if no selectors are defined.
func (s SelectorSpec) IsEmpty() bool { return len(s) == 0 }

// IsSelf returns true if this is a single empty-string selector (self-reference).
func (s SelectorSpec) IsSelf() bool { return len(s) == 1 && s[0] == "" }

// Transform is a post-extraction operation on a leaf string value.
type Transform struct {
	Type      string `yaml:"type" json:"type"`
	Pattern   string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Separator string `yaml:"separator,omitempty" json:"separator,omitempty"`
}

// Known transform types.
var knownTransforms = map[string]bool{
	"trim":      true,
	"regex":     true,
	"lowercase": true,
	"uppercase": true,
	"split":     true,
}

// Load reads and validates a schema file. The format is detected by
// extension: .yaml / .yml → YAML, .json → JSON. Any other extension
// errors out rather than guessing.
func Load(path string) (*Schema, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}

	var s Schema
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("parse yaml %s: %w", path, err)
		}
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return nil, fmt.Errorf("parse json %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("schema %s: unsupported extension (want .yaml, .yml, or .json)", path)
	}

	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("schema %s: %w", path, err)
	}
	return &s, nil
}

// Validate enforces version-specific constraints.
func (s *Schema) Validate() error {
	if s.Version != 1 && s.Version != 2 {
		return fmt.Errorf("version %d is not supported (want 1 or 2)", s.Version)
	}
	if len(s.Fields) == 0 {
		return errors.New("schema has no fields")
	}
	for name, f := range s.Fields {
		if err := validateField(name, f, true, s.Version); err != nil {
			return err
		}
	}
	return nil
}

func validateField(path string, f *Field, topLevel bool, version int) error {
	if f == nil {
		return fmt.Errorf("%s: nil field", path)
	}
	if topLevel && f.Selector.IsEmpty() {
		return fmt.Errorf("%s: top-level fields must have a non-empty selector", path)
	}
	if topLevel && f.Selector.IsSelf() {
		return fmt.Errorf("%s: top-level fields must have a non-empty selector", path)
	}

	// v1 rejects v2-only features.
	if version == 1 {
		if len(f.Selector) > 1 {
			return fmt.Errorf("%s: fallback selectors require version 2 (schema is version 1)", path)
		}
		if len(f.Transforms) > 0 {
			return fmt.Errorf("%s: transforms require version 2 (schema is version 1)", path)
		}
	}

	// Transforms on fields with nested sub-fields don't make sense —
	// transforms operate on leaf string values, not objects.
	if len(f.Transforms) > 0 && len(f.Fields) > 0 {
		return fmt.Errorf("%s: transforms cannot be used on fields with nested sub-fields", path)
	}

	for _, t := range f.Transforms {
		if !knownTransforms[t.Type] {
			return fmt.Errorf("%s: unknown transform type %q", path, t.Type)
		}
		if t.Type == "regex" && t.Pattern == "" {
			return fmt.Errorf("%s: regex transform requires a pattern", path)
		}
		if t.Type == "split" && t.Separator == "" {
			return fmt.Errorf("%s: split transform requires a separator", path)
		}
	}

	for childName, child := range f.Fields {
		if err := validateField(path+"."+childName, child, false, version); err != nil {
			return err
		}
	}
	return nil
}

// Extract runs the schema against an HTML body and returns a nested
// map matching the schema's shape. The base URL is accepted for
// parity with other extractors; v1/v2 do not resolve relative URLs,
// on purpose — consumers can join against Record.CanonicalURL.
//
// Fields whose selector matches nothing are omitted from the output.
// An entirely-unmatched schema returns an empty map, not nil.
func Extract(body []byte, _ string, s *Schema) (map[string]any, error) {
	if s == nil {
		return nil, errors.New("nil schema")
	}
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	return extractFields(doc.Selection, s.Fields), nil
}

func extractFields(scope *goquery.Selection, fields map[string]*Field) map[string]any {
	out := map[string]any{}
	for name, f := range fields {
		if v, ok := extractField(scope, f); ok {
			out[name] = v
		}
	}
	return out
}

func extractField(scope *goquery.Selection, f *Field) (any, bool) {
	var sel *goquery.Selection

	if f.Selector.IsSelf() {
		sel = scope
	} else {
		// Try each fallback selector in order; use first match.
		for _, s := range f.Selector {
			sel = scope.Find(s)
			if sel.Length() > 0 {
				break
			}
		}
	}
	if sel == nil || sel.Length() == 0 {
		return nil, false
	}

	if f.Multiple {
		return extractMultiple(sel, f), true
	}
	return extractOne(sel.First(), f), true
}

func extractMultiple(sel *goquery.Selection, f *Field) []any {
	out := make([]any, 0, sel.Length())
	sel.Each(func(_ int, s *goquery.Selection) {
		out = append(out, extractOne(s, f))
	})
	return out
}

func extractOne(s *goquery.Selection, f *Field) any {
	if len(f.Fields) > 0 {
		return extractFields(s, f.Fields)
	}
	var val string
	if f.Attr != "" {
		v, _ := s.Attr(f.Attr)
		val = strings.TrimSpace(v)
	} else {
		val = strings.TrimSpace(s.Text())
	}
	if len(f.Transforms) > 0 {
		return applyTransforms(val, f.Transforms)
	}
	return val
}

// applyTransforms runs a pipeline of transforms on a string value.
// Most transforms return a string; split returns []string. The return
// type is `any` so the caller can store it directly in the output map.
func applyTransforms(val string, transforms []Transform) any {
	var result any = val
	for _, t := range transforms {
		s, ok := result.(string)
		if !ok {
			// Previous transform changed the type (e.g. split → []string).
			// Remaining string transforms can't apply; stop the pipeline.
			break
		}
		switch t.Type {
		case "trim":
			result = strings.TrimSpace(s)
		case "lowercase":
			result = strings.ToLower(s)
		case "uppercase":
			result = strings.ToUpper(s)
		case "regex":
			re, err := regexp.Compile(t.Pattern)
			if err != nil {
				// Bad regex at runtime — return the value unchanged.
				continue
			}
			m := re.FindStringSubmatch(s)
			if m == nil {
				// No match — return value unchanged.
				continue
			}
			if len(m) > 1 {
				result = m[1] // first capture group
			} else {
				result = m[0] // full match
			}
		case "split":
			result = strings.Split(s, t.Separator)
		}
	}
	return result
}
