package config

import "fmt"

// OnReadsUnavailable is what a leg does when the served read path is not
// serving — a failed self-test, a missing handshake or refused calls.
// degrade records the reason visibly and continues on the supplied prompt;
// halt stops the leg and publishes nothing. Read from the base revision
// like every other policy key, defaulting to degrade.
func (c *Config) OnReadsUnavailable() string {
	if value := c.Get(".policy.on_reads_unavailable"); value != "" {
		return value
	}
	return "degrade"
}

// assertOnReadsUnavailable refuses a third value for a two-value switch.
//
// Read leniently, `on_reads_unavailable: retry` would fall through to
// whichever branch is not `halt`, so a repository that meant to stop a leg
// with no reads would silently degrade instead and nothing would ever say
// so.
func (c *Config) assertOnReadsUnavailable() error {
	switch value := c.Get(".policy.on_reads_unavailable"); value {
	case "", "degrade", "halt":
		return nil
	default:
		return &Refusal{
			Message: fmt.Sprintf("policy.on_reads_unavailable is %q, which is not one of degrade or halt", named(value)),
			Hint:    "It decides whether a leg with no served reads degrades visibly or stops. Set it to degrade or halt in the repository config, or remove it to take the default of degrade.",
		}
	}
}
