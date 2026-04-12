package action

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseInlineClick(t *testing.T) {
	a, err := ParseInline("click:.cookie-accept")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "click" || a.Selector != ".cookie-accept" {
		t.Errorf("got %+v", a)
	}
}

func TestParseInlineWait(t *testing.T) {
	a, err := ParseInline("wait:#content")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "wait" || a.Selector != "#content" {
		t.Errorf("got %+v", a)
	}
}

func TestParseInlineScroll(t *testing.T) {
	a, err := ParseInline("scroll:bottom")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "scroll" || a.Selector != "bottom" {
		t.Errorf("got %+v", a)
	}
}

func TestParseInlineType(t *testing.T) {
	a, err := ParseInline("type:#search:hello world")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "type" || a.Selector != "#search" || a.Value != "hello world" {
		t.Errorf("got %+v", a)
	}
}

func TestParseInlineSleep(t *testing.T) {
	a, err := ParseInline("sleep:2s")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "sleep" || a.Value != "2s" {
		t.Errorf("got %+v", a)
	}
}

func TestParseInlineEvaluate(t *testing.T) {
	a, err := ParseInline("evaluate:document.querySelector('.more').click()")
	if err != nil {
		t.Fatal(err)
	}
	if a.Type != "evaluate" || a.Value != "document.querySelector('.more').click()" {
		t.Errorf("got %+v", a)
	}
}

func TestParseInlineTypeMissingValue(t *testing.T) {
	_, err := ParseInline("type:#input")
	if err == nil {
		t.Error("type without value should fail")
	}
}

func TestParseInlineUnknownType(t *testing.T) {
	_, err := ParseInline("hover:.btn")
	if err == nil {
		t.Error("unknown action type should fail")
	}
}

func TestParseInlineEmpty(t *testing.T) {
	_, err := ParseInline("")
	if err == nil {
		t.Error("empty spec should fail")
	}
}

func TestParseInlineNoArg(t *testing.T) {
	_, err := ParseInline("click")
	if err == nil {
		t.Error("missing arg should fail")
	}
}

func TestValidateRejectsEmptyActions(t *testing.T) {
	seq := &Sequence{Version: 1, Actions: nil}
	if err := seq.Validate(); err == nil {
		t.Error("empty actions should fail validation")
	}
}

func TestValidateRejectsBadVersion(t *testing.T) {
	seq := &Sequence{
		Version: 2,
		Actions: []Action{{Type: "click", Selector: ".btn"}},
	}
	if err := seq.Validate(); err == nil {
		t.Error("version 2 should fail")
	}
}

func TestValidateRejectsClickWithoutSelector(t *testing.T) {
	seq := &Sequence{
		Version: 1,
		Actions: []Action{{Type: "click"}},
	}
	if err := seq.Validate(); err == nil {
		t.Error("click without selector should fail")
	}
}

func TestValidateRejectsSleepBadDuration(t *testing.T) {
	seq := &Sequence{
		Version: 1,
		Actions: []Action{{Type: "sleep", Value: "forever"}},
	}
	if err := seq.Validate(); err == nil {
		t.Error("invalid sleep duration should fail")
	}
}

func TestChromedpActions(t *testing.T) {
	seq := &Sequence{
		Version: 1,
		Actions: []Action{
			{Type: "click", Selector: ".btn"},
			{Type: "wait", Selector: "#result"},
			{Type: "sleep", Value: "500ms"},
			{Type: "scroll", Selector: "bottom"},
			{Type: "evaluate", Value: "console.log('hi')"},
		},
	}
	actions, err := seq.ChromedpActions()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 5 {
		t.Errorf("actions len = %d, want 5", len(actions))
	}
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actions.yaml")
	content := `version: 1
actions:
  - type: click
    selector: ".cookie-banner .accept"
  - type: wait
    selector: "#content"
  - type: sleep
    value: "1s"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	seq, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(seq.Actions) != 3 {
		t.Errorf("actions len = %d, want 3", len(seq.Actions))
	}
	if seq.Actions[0].Type != "click" {
		t.Errorf("actions[0].type = %q, want click", seq.Actions[0].Type)
	}
}

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actions.json")
	content := `{"version":1,"actions":[{"type":"click","selector":".btn"}]}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	seq, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(seq.Actions) != 1 {
		t.Errorf("actions len = %d, want 1", len(seq.Actions))
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	content := `version: 1
actions:
  - type: click
    selector: ".btn"
    bogus: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("should reject unknown fields")
	}
}
