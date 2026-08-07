package workflowdef

// KnownRoles is the fixed, closed set of role names a Workflow
// Definition's `roles:` list may contain. Closed because a role maps
// 1:1 onto a Planner/Coder/Reviewer state in
// docs/01-run-state-machine.md — adding a role means adding a state,
// a bigger change than a config edit, so this isn't a persisted/
// UI-editable list the way internal/workers is.
var KnownRoles = []string{"planner", "coder", "reviewer"}

// IsKnownRole reports whether name is a member of KnownRoles.
func IsKnownRole(name string) bool {
	for _, r := range KnownRoles {
		if r == name {
			return true
		}
	}
	return false
}
