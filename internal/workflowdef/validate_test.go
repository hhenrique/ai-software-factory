package workflowdef

import "testing"

func TestValidateReferenceDefinitionsAreClean(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"issue-to-pr-standard", IssueToPRStandardYAML},
		{"dependency-bump-minimal", DependencyBumpMinimalYAML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def, err := Parse(tc.data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if errs := Validate(def); len(errs) != 0 {
				t.Fatalf("Validate: expected no errors, got:\n%s", errs.Error())
			}
		})
	}
}

func TestValidateBrokenFixturesFailWithExpectedRule(t *testing.T) {
	for _, bf := range brokenFixtures() {
		t.Run(bf.Name, func(t *testing.T) {
			def, err := Parse(bf.YAML)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			errs := Validate(def)
			if len(errs) == 0 {
				t.Fatalf("Validate: expected at least one error for %s, got none", bf.Name)
			}
			if !errs.HasRule(bf.Rule) {
				t.Fatalf("Validate: expected an error tagged %s for %s, got:\n%s", bf.Rule, bf.Name, errs.Error())
			}
		})
	}
}
