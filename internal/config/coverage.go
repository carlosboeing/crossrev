package config

import (
	"github.com/carlosboeing/crossrev/internal/core"
)

// Coverage is the ledger storage configuration, with the defaults applied at
// read time. They are applied here and not in Defaults() because the frozen
// oracle pins the merged object byte for byte.
type Coverage struct {
	Store        string // auto | refs | marker
	RefNamespace string // refs/crossrev
	OnOverflow   string // degrade | halt
}

// Coverage reads the ledger storage configuration out of the merge. An absent
// value reads as the default, the way every other key with a default
// underneath it does; a present one was already refused at load when it is not
// one CrossRev recognises.
func (c *Config) Coverage() Coverage {
	out := Coverage{
		Store:        c.Get(".coverage.store"),
		RefNamespace: c.Get(".coverage.ref_namespace"),
		OnOverflow:   c.Get(".coverage.on_overflow"),
	}
	if out.Store == "" {
		out.Store = "auto"
	}
	if out.RefNamespace == "" {
		out.RefNamespace = "refs/crossrev"
	}
	if out.OnOverflow == "" {
		out.OnOverflow = "degrade"
	}
	return out
}

// Reviewer is one reviewer slot, fully resolved. It is the only route to the
// reviewer's harness, model, effort and endpoint: reading .reviewer.* directly
// would accept a plural configuration and then run the singular default.
type Reviewer struct{ ID, Harness, Model, Effort, Endpoint string }

// Reviewers is the canonical list. `reviewer:` maps to a single slot with id
// "reviewer1". Ids are explicit and stable and never derived from list
// position: an operator reordering their config would otherwise orphan every
// stored ref.
//
// The list wins over the shorthand whenever it names a slot, because the
// shorthand is always present underneath it — Defaults() carries one — and
// reading the shorthand first would run codex beside a config that names
// claude. An empty or absent list is not a configuration at all, so the
// shorthand answers; the loader refuses an empty list explicitly, and this
// stays total for callers that never passed through it.
func (c *Config) Reviewers() []Reviewer {
	if list, ok := lookup(c.Merged, ".reviewers").([]any); ok && len(list) > 0 {
		slots := make([]Reviewer, 0, len(list))
		for _, item := range list {
			slot, _ := item.(*Object)
			resolved := Reviewer{ID: "reviewer1", Harness: string(core.HarnessCodex)}
			if slot != nil {
				if id := alternative(slot.Value("id")); id != "" {
					resolved.ID = id
				}
				if harness := alternative(slot.Value("harness")); harness != "" {
					resolved.Harness = harness
				}
				resolved.Model = alternative(slot.Value("model"))
				resolved.Effort = alternative(slot.Value("effort"))
				resolved.Endpoint = alternative(slot.Value("endpoint"))
			}
			slots = append(slots, resolved)
		}
		return slots
	}
	harness := c.Get(".reviewer.harness")
	if harness == "" {
		harness = string(core.HarnessCodex)
	}
	return []Reviewer{{
		ID:       "reviewer1",
		Harness:  harness,
		Model:    c.Get(".reviewer.model"),
		Effort:   c.Get(".reviewer.effort"),
		Endpoint: c.Get(".reviewer.endpoint"),
	}}
}
