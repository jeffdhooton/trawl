// Package extract runs user-specified selectors against a parsed HTML
// document and returns a map of results suitable for JSON encoding.
//
// P0 ships CSS selectors only (goquery). XPath and JSONPath land in P1.
package extract

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Field describes a single named extraction rule.
type Field struct {
	Name     string
	Selector string
	// Attr, when set, pulls the given attribute instead of text content.
	Attr string
	// Multiple, when true, returns a []string of all matches instead of the first.
	Multiple bool
	// NoTrim disables the default whitespace trimming on extracted values.
	NoTrim bool
}

// CSS runs a set of fields against an HTML body and returns a map keyed by
// field name. Missing fields are omitted (not null) so the output stays tight.
func CSS(body []byte, fields []Field) (map[string]any, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	out := make(map[string]any, len(fields))
	for _, f := range fields {
		if f.Selector == "" {
			return nil, fmt.Errorf("field %q: empty selector", f.Name)
		}
		sel := doc.Find(f.Selector)
		if sel.Length() == 0 {
			continue
		}

		valueFor := func(node *goquery.Selection) string {
			var v string
			if f.Attr != "" {
				v, _ = node.Attr(f.Attr)
			} else {
				v = node.Text()
			}
			if !f.NoTrim {
				v = strings.TrimSpace(v)
			}
			return v
		}

		if f.Multiple {
			vals := make([]string, 0, sel.Length())
			sel.Each(func(_ int, s *goquery.Selection) {
				vals = append(vals, valueFor(s))
			})
			out[f.Name] = vals
		} else {
			out[f.Name] = valueFor(sel.First())
		}
	}
	return out, nil
}

// ParseFieldSpec parses the compact CLI form used by `trawl scrape --selector`:
//
//	"name=selector"                  → text content of first match
//	"name=selector@attr"             → attribute value of first match
//	"name=selector[]"                → text content of all matches
//	"name=selector[]@attr"           → attribute value of all matches
func ParseFieldSpec(spec string) (Field, error) {
	name, sel, ok := strings.Cut(spec, "=")
	if !ok || name == "" || sel == "" {
		return Field{}, fmt.Errorf("invalid field spec %q: expected name=selector", spec)
	}

	f := Field{Name: strings.TrimSpace(name)}
	sel = strings.TrimSpace(sel)

	// Extract trailing @attr (everything after the LAST unescaped @).
	if idx := strings.LastIndex(sel, "@"); idx >= 0 {
		f.Attr = sel[idx+1:]
		sel = sel[:idx]
	}

	// Extract trailing [] marker for multiple.
	if strings.HasSuffix(sel, "[]") {
		f.Multiple = true
		sel = strings.TrimSuffix(sel, "[]")
	}

	f.Selector = strings.TrimSpace(sel)
	if f.Selector == "" {
		return Field{}, fmt.Errorf("invalid field spec %q: empty selector", spec)
	}
	return f, nil
}
