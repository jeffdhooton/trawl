// Package schema implements declarative structured extraction — a
// YAML or JSON file describes a nested shape of selectors, and
// Extract walks an HTML document to produce a map matching that
// shape. It's the v1 answer to "Firecrawl schema extract" without
// any LLM in the loop.
//
// v1 scope (deliberately narrow):
//   - selector + optional attr → string
//   - multiple: true → array of strings, or array of objects if
//     nested fields are declared
//   - nested fields under `fields:` → object, scoped to the match
//   - empty selector inside a nested context = "self" (the iterated
//     parent element) — required for the common "array of objects
//     where each object needs the link's text and href" case
//   - missing matches → field omitted from output
//
// NOT in v1 (easy to add when a real consumer asks):
//   - transforms (trim, regex, number coercion)
//   - required / optional validation
//   - reference / include for sub-schema reuse
//   - conditional logic, index modifiers beyond multiple
//
// The `version: 1` field in the schema is required so a future
// breaking change has an explicit escape hatch.
package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"gopkg.in/yaml.v3"
)

// Schema is the top-level structured-extraction spec.
type Schema struct {
	// Version is required and must be 1. Exists so a future breaking
	// change can tell which parser to use without reinventing the
	// schema format entirely.
	Version int `yaml:"version" json:"version"`
	// Fields maps output-key → Field spec. The top-level fields all
	// operate on the document scope.
	Fields map[string]*Field `yaml:"fields" json:"fields"`
}

// Field is one extraction rule. An empty Selector is only valid
// inside a nested Fields block, where it means "the current iterated
// parent element" (self).
type Field struct {
	Selector string            `yaml:"selector" json:"selector"`
	Attr     string            `yaml:"attr,omitempty" json:"attr,omitempty"`
	Multiple bool              `yaml:"multiple,omitempty" json:"multiple,omitempty"`
	Fields   map[string]*Field `yaml:"fields,omitempty" json:"fields,omitempty"`
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

// Validate enforces the v1 constraints that can't be expressed in
// the type system:
//   - Version must be 1.
//   - Fields map must be non-empty.
//   - Top-level fields must have a non-empty selector (empty selector
//     is only meaningful inside a nested parent scope).
//   - Nested fields may have an empty selector (self reference).
func (s *Schema) Validate() error {
	if s.Version != 1 {
		return fmt.Errorf("version %d is not supported (want 1)", s.Version)
	}
	if len(s.Fields) == 0 {
		return errors.New("schema has no fields")
	}
	for name, f := range s.Fields {
		if err := validateField(name, f, true); err != nil {
			return err
		}
	}
	return nil
}

func validateField(path string, f *Field, topLevel bool) error {
	if f == nil {
		return fmt.Errorf("%s: nil field", path)
	}
	if topLevel && f.Selector == "" {
		return fmt.Errorf("%s: top-level fields must have a non-empty selector", path)
	}
	for childName, child := range f.Fields {
		if err := validateField(path+"."+childName, child, false); err != nil {
			return err
		}
	}
	return nil
}

// Extract runs the schema against an HTML body and returns a nested
// map matching the schema's shape. The base URL is accepted for
// parity with other extractors; v1 does not resolve relative URLs,
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

// extractFields is the recursive workhorse. scope is the goquery
// Selection relative to which each field's selector is evaluated.
// At the top level scope is the whole document; inside a nested
// Multiple=true iteration scope is the single iterated element.
func extractFields(scope *goquery.Selection, fields map[string]*Field) map[string]any {
	out := map[string]any{}
	for name, f := range fields {
		if v, ok := extractField(scope, f); ok {
			out[name] = v
		}
	}
	return out
}

// extractField resolves one field against the given scope. Returns
// (value, true) on a match, (nil, false) when the field has no
// matches — the caller drops unmatched fields from the output map.
func extractField(scope *goquery.Selection, f *Field) (any, bool) {
	// Empty selector inside a nested context = self. "Self" here means
	// the scope element itself, not its descendants.
	var sel *goquery.Selection
	if f.Selector == "" {
		sel = scope
	} else {
		sel = scope.Find(f.Selector)
	}
	if sel.Length() == 0 {
		return nil, false
	}

	if f.Multiple {
		return extractMultiple(sel, f), true
	}
	// Single-match: the first element.
	return extractOne(sel.First(), f), true
}

// extractMultiple handles `multiple: true`. Each matched element
// becomes either a string (when no sub-fields are declared) or an
// object (when sub-fields ARE declared). Empty elements are kept —
// we do not silently drop "" entries, because that would hide a
// buggy selector.
func extractMultiple(sel *goquery.Selection, f *Field) []any {
	out := make([]any, 0, sel.Length())
	sel.Each(func(_ int, s *goquery.Selection) {
		out = append(out, extractOne(s, f))
	})
	return out
}

// extractOne turns a single Selection into either a value (attr or
// text) or an object (when sub-fields are declared).
func extractOne(s *goquery.Selection, f *Field) any {
	if len(f.Fields) > 0 {
		return extractFields(s, f.Fields)
	}
	if f.Attr != "" {
		v, _ := s.Attr(f.Attr)
		return strings.TrimSpace(v)
	}
	return strings.TrimSpace(s.Text())
}
