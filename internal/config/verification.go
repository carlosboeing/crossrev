package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The verification defaults: no app named means the GitHub Actions app,
// and a review waits ten minutes for its required checks.
const (
	DefaultRequiredCheckApp = "github-actions"
	DefaultWaitMinutes      = 10
	MaxWaitMinutes          = 30
)

// OwnCheckNames are the check runs CrossRev's own generated workflows
// publish: each workflow's name over its leg job, the way GitHub names a
// check run where the job sets no name. A review that waited for one of
// these would wait for itself, so required checks naming one are refused.
//
// The literals live here because this package may not import the
// generator; a test binds them to its templates instead, so a rename on
// either side fails there rather than gating a review on itself.
//
// It answers a fresh slice each time, for the reason
// ForgeCredentialNames does: an exported slice variable is writable from
// any package in the binary, and shortening this one would admit the
// self-gating it exists to refuse.
func OwnCheckNames() []string {
	return []string{"crossrev review / review", "crossrev resolve / resolve"}
}

// RequiredCheck is one entry of verification.required_checks: a check run
// name and the app that publishes it.
type RequiredCheck struct {
	Name string
	App  string
}

// String renders the check as NAME, or NAME@APP where the app is not the
// default. It is the form the run log records and the flag accepts.
func (c RequiredCheck) String() string {
	if c.App == "" || c.App == DefaultRequiredCheckApp {
		return c.Name
	}
	return c.Name + "@" + c.App
}

// ParseRequiredCheck parses one NAME[@APP] item — a config string or one
// --required-check flag. The app defaults to github-actions. It is the
// one item parser both surfaces pass through.
func ParseRequiredCheck(item string) (RequiredCheck, error) {
	name, app := item, DefaultRequiredCheckApp
	// The app is what follows the last @, so a check name holding an @ of
	// its own survives: an app slug never holds one.
	if at := strings.LastIndex(item, "@"); at >= 0 {
		name, app = item[:at], item[at+1:]
	}
	if name == "" {
		return RequiredCheck{}, errors.New("empty check name")
	}
	if app == "" {
		return RequiredCheck{}, fmt.Errorf("empty app name in %q", item)
	}
	return RequiredCheck{Name: name, App: app}, nil
}

// NormalizeRequiredChecks validates parsed checks — config items or flag
// values — refusing duplicates and CrossRev's own jobs. A duplicate is
// the same name from the same app: `build` and `build@github-actions`
// name one check. It is the one list validator both surfaces pass
// through.
func NormalizeRequiredChecks(checks []RequiredCheck) ([]RequiredCheck, error) {
	seen := map[RequiredCheck]bool{}
	for _, check := range checks {
		if seen[check] {
			return nil, fmt.Errorf("duplicate required check %q", check.String())
		}
		seen[check] = true
		for _, own := range OwnCheckNames() {
			if check.Name == own {
				leg := "review"
				if strings.Contains(own, "resolve") {
					leg = "resolve"
				}
				return nil, fmt.Errorf("%q is CrossRev's own %s job, which a review cannot wait for", own, leg)
			}
		}
	}
	return append([]RequiredCheck(nil), checks...), nil
}

// ParseWaitMinutes validates a wait in minutes from the config or the
// flag. A nil value is absent and reads as the default; anything else
// must be a whole number of minutes from 0 to 30. It is the one validator
// both surfaces pass through.
func ParseWaitMinutes(value any) (int, error) {
	if value == nil {
		return DefaultWaitMinutes, nil
	}
	text, ok := value.(string)
	if !ok {
		if number, isNumber := value.(Number); isNumber {
			text = string(number)
		} else {
			return 0, fmt.Errorf("%s is not a whole number of minutes from 0 to 30", shapeOf(value))
		}
	}
	if !allDigits(text) {
		return 0, fmt.Errorf("%q is not a whole number of minutes from 0 to 30", text)
	}
	minutes, err := strconv.Atoi(text)
	if err != nil || minutes > MaxWaitMinutes {
		return 0, fmt.Errorf("%q is more than the 30 minute wait", text)
	}
	return minutes, nil
}

// allDigits reports whether text is one or more ASCII digits. It is
// deliberately text-first: `10.5`, `-1` and `ten` all fail before any
// arithmetic runs.
func allDigits(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

// Verification is the verification configuration, with the defaults
// applied at read time. They are applied here and not in Defaults()
// because the frozen oracle pins the merged object byte for byte.
type Verification struct {
	RequiredChecks []RequiredCheck
	WaitMinutes    int
}

// Verification reads the verification configuration out of the merge. An
// absent value reads as the default, the way every other key with a
// default underneath it does; a present one was already refused at load
// when it is not one CrossRev recognises.
func (c *Config) Verification() Verification {
	out := Verification{WaitMinutes: DefaultWaitMinutes}
	if raw := lookup(c.Merged, ".verification.required_checks"); raw != nil {
		if list, ok := raw.([]any); ok {
			if checks, err := decodeRequiredChecks(list); err == nil {
				out.RequiredChecks = checks
			}
		}
	}
	if minutes, err := ParseWaitMinutes(lookup(c.Merged, ".verification.wait_minutes")); err == nil {
		out.WaitMinutes = minutes
	}
	return out
}

// decodeRequiredChecks reads config items — strings or name/app mappings —
// into parsed checks. Shape faults name the item; value faults come from
// the shared validators.
func decodeRequiredChecks(list []any) ([]RequiredCheck, error) {
	checks := make([]RequiredCheck, 0, len(list))
	for i, item := range list {
		switch entry := item.(type) {
		case string:
			check, err := ParseRequiredCheck(entry)
			if err != nil {
				return nil, err
			}
			checks = append(checks, check)
		case *Object:
			check, err := decodeRequiredCheckMapping(entry)
			if err != nil {
				return nil, err
			}
			checks = append(checks, check)
		default:
			return nil, fmt.Errorf("item %d is %s, which is not a check name or a name/app mapping", i+1, shapeOf(item))
		}
	}
	return NormalizeRequiredChecks(checks)
}

// decodeRequiredCheckMapping reads one {name, app} mapping. The name is
// required; the app defaults to github-actions.
func decodeRequiredCheckMapping(mapping *Object) (RequiredCheck, error) {
	name, _ := mapping.Value("name").(string)
	if name == "" {
		return RequiredCheck{}, fmt.Errorf("a required check mapping names no check")
	}
	app := DefaultRequiredCheckApp
	if raw := mapping.Value("app"); raw != nil {
		text, ok := raw.(string)
		if !ok || text == "" {
			return RequiredCheck{}, fmt.Errorf("empty app name for %q", name)
		}
		app = text
	}
	return RequiredCheck{Name: name, App: app}, nil
}

// assertVerification refuses required checks and a wait CrossRev does not
// implement.
//
// Read leniently, `wait_minutes: ten` would wait by a default nobody
// stated while the config says ten, and a required check naming
// CrossRev's own review job would gate the review on itself.
func (c *Config) assertVerification() error {
	if err := requireMappingAt(c.Merged, ".verification"); err != nil {
		return err
	}
	if raw := lookup(c.Merged, ".verification.required_checks"); raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return &Refusal{
				Message: "verification.required_checks is " + shapeOf(raw) + ", which is not a list",
				Hint:    "It names the check runs a review waits for before judging, each a NAME or NAME@APP string or a name/app mapping. Set it to a list in the repository config, or remove it to wait for none.",
			}
		}
		if _, err := decodeRequiredChecks(list); err != nil {
			return &Refusal{
				Message: "verification.required_checks: " + err.Error(),
				Hint:    "Each entry is a NAME or NAME@APP string or a name/app mapping, naming a check run that is not CrossRev's own review or resolve job. Correct it where it is set, or remove it to wait for none.",
			}
		}
	}
	if _, err := ParseWaitMinutes(lookup(c.Merged, ".verification.wait_minutes")); err != nil {
		return &Refusal{
			Message: "verification.wait_minutes: " + err.Error(),
			Hint:    "It bounds how long a review waits for its required checks before judging without them. Set it from 0 to 30 in the repository config, or remove it to take the default of 10.",
		}
	}
	return nil
}
