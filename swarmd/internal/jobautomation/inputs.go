package jobautomation

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Input declares a reusable workflow parameter. A nil default requires with.
type Input struct {
	Description string  `json:"description"`
	Default     *string `json:"default,omitempty"`
}

var inputName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
var inputPlaceholder = regexp.MustCompile(`\{\{inputs\.([^{}]*)\}\}`)

// RenderInputs returns an independent executable definition. Replacements are
// literal and single-pass: input values cannot introduce more input references.
func (a Automation) RenderInputs() (Automation, error) {
	if a.inputsRendered {
		return a, nil
	}
	values := map[string]string{}
	if len(a.Inputs) > 20 {
		return Automation{}, invalid("at most 20 inputs are allowed")
	}
	for name, decl := range a.Inputs {
		if !inputName.MatchString(name) {
			return Automation{}, invalid("invalid input name %q", name)
		}
		if decl.Default != nil && utf8.RuneCountInString(*decl.Default) > 1000 {
			return Automation{}, invalid("input %q default exceeds 1000 characters", name)
		}
		value, ok := a.With[name]
		if !ok && decl.Default != nil {
			value, ok = *decl.Default, true
		}
		if !ok {
			return Automation{}, invalid("input %q requires a with value", name)
		}
		if utf8.RuneCountInString(value) > 1000 {
			return Automation{}, invalid("input %q exceeds 1000 characters", name)
		}
		values[name] = value
	}
	for name := range a.With {
		if _, ok := a.Inputs[name]; !ok {
			return Automation{}, invalid("with names undeclared input %q", name)
		}
	}
	data, err := json.Marshal(a)
	if err != nil {
		return Automation{}, err
	}
	var out Automation
	if err := json.Unmarshal(data, &out); err != nil {
		return Automation{}, err
	}
	var renderErr error
	replace := func(s string) string {
		return inputPlaceholder.ReplaceAllStringFunc(s, func(match string) string {
			name := inputPlaceholder.FindStringSubmatch(match)[1]
			value, ok := values[name]
			if !ok {
				renderErr = invalid("unknown input placeholder %s", match)
			}
			return value
		})
	}
	for i := range out.Steps {
		s := &out.Steps[i]
		s.Brief, s.Worker, s.Message, s.Path = replace(s.Brief), replace(s.Worker), replace(s.Message), replace(s.Path)
		for j := range s.Args {
			s.Args[j] = replace(s.Args[j])
		}
	}
	if out.On != nil {
		t := out.On
		t.Event.URL, t.Event.Items, t.Event.ID = replace(t.Event.URL), replace(t.Event.Items), replace(t.Event.ID)
		t.Start.Worker, t.Start.Brief = replace(t.Start.Worker), replace(t.Start.Brief)
	}
	out.inputsRendered = true
	return out, renderErr
}

// normalizeInputs validates the executable form but retains the reusable fields.
func (a *Automation) normalizeInputs() error {
	rendered, err := a.RenderInputs()
	if err != nil {
		return err
	}
	if err := rendered.normalize(); err != nil {
		return err
	}
	for i := range a.Steps {
		original := a.Steps[i]
		s := &rendered.Steps[i]
		if strings.Contains(original.Brief, "{{inputs.") {
			s.Brief = original.Brief
		}
		if strings.Contains(original.Worker, "{{inputs.") {
			s.Worker = original.Worker
		}
		if strings.Contains(original.Message, "{{inputs.") {
			s.Message = original.Message
		}
		if strings.Contains(original.Path, "{{inputs.") {
			s.Path = original.Path
		}
		s.Args = original.Args
	}
	rendered.On = a.On
	rendered.inputsRendered = false
	*a = rendered
	return nil
}
