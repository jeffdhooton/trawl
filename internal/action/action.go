// Package action defines pre-scrape interactive actions for chromium.
// Actions run after the page has loaded and settled, before DOM
// capture. They let users dismiss cookie banners, scroll to lazy
// content, fill forms, or run arbitrary JS.
//
// Two input paths: inline flags (--action "click:.btn") parsed by
// ParseInline, or a YAML/JSON file (--actions file.yaml) loaded by
// Load. Both produce a Sequence that converts to chromedp actions.
package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"gopkg.in/yaml.v3"
)

// Action is one pre-scrape interaction step.
type Action struct {
	Type     string `yaml:"type" json:"type"`
	Selector string `yaml:"selector,omitempty" json:"selector,omitempty"`
	Value    string `yaml:"value,omitempty" json:"value,omitempty"`
}

// Sequence is an ordered list of actions with a version field for
// forward compatibility.
type Sequence struct {
	Version int      `yaml:"version" json:"version"`
	Actions []Action `yaml:"actions" json:"actions"`
}

// Load reads and validates an actions file. Format detected by extension.
func Load(path string) (*Sequence, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read actions: %w", err)
	}

	var seq Sequence
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&seq); err != nil {
			return nil, fmt.Errorf("parse yaml %s: %w", path, err)
		}
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&seq); err != nil {
			return nil, fmt.Errorf("parse json %s: %w", path, err)
		}
	default:
		return nil, fmt.Errorf("actions %s: unsupported extension (want .yaml, .yml, or .json)", path)
	}

	if err := seq.Validate(); err != nil {
		return nil, fmt.Errorf("actions %s: %w", path, err)
	}
	return &seq, nil
}

// ParseInline parses an inline action spec like "click:.btn" or
// "type:#search:hello". The format is type:arg1[:arg2].
func ParseInline(spec string) (Action, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 || parts[0] == "" {
		return Action{}, fmt.Errorf("invalid action %q: want type:arg (e.g. click:.btn)", spec)
	}

	a := Action{Type: parts[0]}
	switch a.Type {
	case "click", "wait", "scroll":
		a.Selector = parts[1]
	case "sleep", "evaluate":
		a.Value = parts[1]
	case "type":
		if len(parts) < 3 {
			return Action{}, fmt.Errorf("type action needs selector and text: type:#input:hello")
		}
		a.Selector = parts[1]
		a.Value = parts[2]
	default:
		return Action{}, fmt.Errorf("unknown action type %q (want click, wait, scroll, type, sleep, evaluate)", a.Type)
	}
	return a, nil
}

// Validate checks the sequence for structural correctness.
func (seq *Sequence) Validate() error {
	if seq.Version != 1 {
		return fmt.Errorf("version %d is not supported (want 1)", seq.Version)
	}
	if len(seq.Actions) == 0 {
		return errors.New("actions list is empty")
	}
	for i, a := range seq.Actions {
		if err := a.validate(); err != nil {
			return fmt.Errorf("action[%d]: %w", i, err)
		}
	}
	return nil
}

func (a *Action) validate() error {
	switch a.Type {
	case "click", "wait":
		if a.Selector == "" {
			return fmt.Errorf("%s requires a selector", a.Type)
		}
	case "scroll":
		if a.Selector == "" {
			return errors.New("scroll requires a selector or \"bottom\"")
		}
	case "type":
		if a.Selector == "" {
			return errors.New("type requires a selector")
		}
		if a.Value == "" {
			return errors.New("type requires a value (text to enter)")
		}
	case "sleep":
		if a.Value == "" {
			return errors.New("sleep requires a duration (e.g. 2s)")
		}
		if _, err := time.ParseDuration(a.Value); err != nil {
			return fmt.Errorf("sleep duration %q: %w", a.Value, err)
		}
	case "evaluate":
		if a.Value == "" {
			return errors.New("evaluate requires a JS expression")
		}
	case "":
		return errors.New("action type is required")
	default:
		return fmt.Errorf("unknown action type %q (want click, wait, scroll, type, sleep, evaluate)", a.Type)
	}
	return nil
}

// ChromedpActions converts the sequence to chromedp actions that can
// be spliced into the chromium engine's action pipeline.
func (seq *Sequence) ChromedpActions() ([]chromedp.Action, error) {
	out := make([]chromedp.Action, 0, len(seq.Actions))
	for i, a := range seq.Actions {
		ca, err := a.chromedpAction()
		if err != nil {
			return nil, fmt.Errorf("action[%d]: %w", i, err)
		}
		out = append(out, ca)
	}
	return out, nil
}

func (a *Action) chromedpAction() (chromedp.Action, error) {
	switch a.Type {
	case "click":
		return chromedp.Click(a.Selector, chromedp.ByQuery), nil
	case "wait":
		return chromedp.WaitVisible(a.Selector, chromedp.ByQuery), nil
	case "scroll":
		if a.Selector == "bottom" {
			return chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil), nil
		}
		return chromedp.ScrollIntoView(a.Selector, chromedp.ByQuery), nil
	case "type":
		return chromedp.SendKeys(a.Selector, a.Value, chromedp.ByQuery), nil
	case "sleep":
		d, err := time.ParseDuration(a.Value)
		if err != nil {
			return nil, fmt.Errorf("sleep duration: %w", err)
		}
		return chromedp.Sleep(d), nil
	case "evaluate":
		return chromedp.Evaluate(a.Value, nil), nil
	default:
		return nil, fmt.Errorf("unknown action type %q", a.Type)
	}
}
