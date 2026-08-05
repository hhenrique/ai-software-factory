package workflowdef

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTargetUnmarshalScalar(t *testing.T) {
	var t1 Target
	if err := yaml.Unmarshal([]byte("execute"), &t1); err != nil {
		t.Fatalf("unmarshal scalar target: %v", err)
	}
	if t1.Destination() != "execute" {
		t.Errorf("Destination() = %q, want %q", t1.Destination(), "execute")
	}
	if t1.HasSideEffect() {
		t.Errorf("HasSideEffect() = true for scalar target, want false")
	}
}

func TestTargetUnmarshalCompound(t *testing.T) {
	var t1 Target
	yamlDoc := "action: task.create(source=review-finding)\nnext: review\n"
	if err := yaml.Unmarshal([]byte(yamlDoc), &t1); err != nil {
		t.Fatalf("unmarshal compound target: %v", err)
	}
	if t1.Destination() != "review" {
		t.Errorf("Destination() = %q, want %q", t1.Destination(), "review")
	}
	if !t1.HasSideEffect() {
		t.Errorf("HasSideEffect() = false for compound target, want true")
	}
	if t1.Action != "task.create(source=review-finding)" {
		t.Errorf("Action = %q, unexpected", t1.Action)
	}
}

func TestTargetUnmarshalInStepOn(t *testing.T) {
	yamlDoc := `
id: review
type: agent
role: reviewer
on:
  approved: merge
  out_of_scope:
    action: task.create(source=review-finding)
    next: review
`
	var s Step
	if err := yaml.Unmarshal([]byte(yamlDoc), &s); err != nil {
		t.Fatalf("unmarshal step: %v", err)
	}
	if got := s.On["approved"].Destination(); got != "merge" {
		t.Errorf("on[approved].Destination() = %q, want %q", got, "merge")
	}
	oos := s.On["out_of_scope"]
	if !oos.HasSideEffect() || oos.Destination() != "review" {
		t.Errorf("on[out_of_scope] = %+v, want side-effecting target to review", oos)
	}
}
