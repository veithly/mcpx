package server

import (
	"encoding/json"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Diagnostics only. The full resolved schema above remains the authority; a
// branch explanation never makes an invalid request valid or changes its data.
func branchDiagnostics(raw []byte) func(map[string]any) string {
	var root struct {
		OneOf []json.RawMessage `json:"oneOf"`
	}
	_ = json.Unmarshal(raw, &root)
	type check struct {
		action   string
		required []string
		validate func(any) error
	}
	checks := []check{}
	for _, rawBranch := range root.OneOf {
		var schema jsonschema.Schema
		if json.Unmarshal(rawBranch, &schema) != nil {
			continue
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			continue
		}
		var description struct {
			Properties map[string]struct {
				Const string `json:"const"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		_ = json.Unmarshal(rawBranch, &description)
		checks = append(checks, check{action: description.Properties["action"].Const, required: description.Required, validate: resolved.Validate})
	}
	return func(args map[string]any) string {
		action, _ := args["action"].(string)
		messages := []string{}
		matched := 0
		required := []string{}
		for _, check := range checks {
			if check.action != "" && check.action != action {
				continue
			}
			required = append(required, check.required...)
			if err := check.validate(args); err != nil {
				if len(messages) < 4 {
					messages = append(messages, err.Error())
				}
			} else {
				matched++
			}
		}
		if matched > 1 {
			return "request forms are mutually exclusive; choose one target form from " + strings.Join(required, ", ")
		}
		if len(messages) > 0 {
			return strings.Join(messages, "; ")
		}
		return "Select one supported action and provide only the fields from that action's current schema."
	}
}
