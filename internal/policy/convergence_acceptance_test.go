package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/carlosboeing/crossrev/internal/policy"
)

// convergenceAcceptanceFile is the hand-authored predicate oracle from
// tests/fixtures/intelligence/convergence.json: the all-clear input and one
// single-obligation weakening per case, each with its expected outcome. It
// is literal data, never generated from the production predicate.
type convergenceAcceptanceFile struct {
	AllClear struct {
		UnresolvedFixable    int  `json:"unresolved_fixable"`
		LedgerCurrent        bool `json:"ledger_current"`
		Required             int  `json:"required"`
		Covered              int  `json:"covered"`
		Outstanding          int  `json:"outstanding"`
		CouldNotReview       int  `json:"could_not_review"`
		ScopeReported        bool `json:"scope_reported"`
		ConfirmationRequired bool `json:"confirmation_required"`
		ConfirmationComplete bool `json:"confirmation_complete"`
		Converged            bool `json:"converged"`
	} `json:"all_clear"`
	Weakened []struct {
		Name      string         `json:"name"`
		Field     string         `json:"field"`
		Fields    map[string]int `json:"fields"`
		Value     any            `json:"value"`
		Converged bool           `json:"converged"`
	} `json:"weakened"`
}

func loadConvergenceAcceptance(t *testing.T) convergenceAcceptanceFile {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above the test directory")
		}
		dir = parent
	}
	raw, err := os.ReadFile(filepath.Join(dir, "tests", "fixtures", "intelligence", "convergence.json"))
	if err != nil {
		t.Fatalf("read the convergence oracle: %v", err)
	}
	var oracle convergenceAcceptanceFile
	if err := json.Unmarshal(raw, &oracle); err != nil {
		t.Fatalf("decode the convergence oracle: %v", err)
	}
	return oracle
}

func acceptanceInput(oracle convergenceAcceptanceFile) policy.Convergence {
	a := oracle.AllClear
	return policy.Convergence{
		UnresolvedFixable:    a.UnresolvedFixable,
		LedgerCurrent:        a.LedgerCurrent,
		Required:             a.Required,
		Covered:              a.Covered,
		Outstanding:          a.Outstanding,
		CouldNotReview:       a.CouldNotReview,
		ScopeReported:        a.ScopeReported,
		ConfirmationRequired: a.ConfirmationRequired,
		ConfirmationComplete: a.ConfirmationComplete,
	}
}

// TestConvergenceAcceptanceOracle replays the frozen predicate oracle: the
// all-clear input converges, and weakening any single obligation refuses.
// It fails on the pre-slice code because no ledger, coverage count, scope
// report or confirmation guard exists there.
func TestConvergenceAcceptanceOracle(t *testing.T) {
	oracle := loadConvergenceAcceptance(t)
	if len(oracle.Weakened) != 10 {
		t.Fatalf("oracle holds %d weakened cases, want 10", len(oracle.Weakened))
	}
	if got := policy.Converged(acceptanceInput(oracle)); got != oracle.AllClear.Converged {
		t.Fatalf("all-clear converges = %v, want %v", got, oracle.AllClear.Converged)
	}
	for _, w := range oracle.Weakened {
		t.Run(w.Name, func(t *testing.T) {
			input := acceptanceInput(oracle)
			switch w.Field {
			case "unresolved_fixable":
				input.UnresolvedFixable = int(asFloat(w.Value))
			case "ledger_current":
				input.LedgerCurrent = asBool(w.Value)
			case "covered":
				input.Covered = int(asFloat(w.Value))
			case "outstanding":
				input.Outstanding = int(asFloat(w.Value))
			case "could_not_review":
				input.CouldNotReview = int(asFloat(w.Value))
			case "scope_reported":
				input.ScopeReported = asBool(w.Value)
			case "confirmation_required":
				input.ConfirmationRequired = asBool(w.Value)
			default:
				if w.Field != "" {
					t.Fatalf("oracle case %q names unknown field %q", w.Name, w.Field)
				}
				for field, value := range w.Fields {
					switch field {
					case "required":
						input.Required = int(value)
					case "covered":
						input.Covered = int(value)
					case "confirmation_required":
						input.ConfirmationRequired = value != 0
					case "confirmation_complete":
						input.ConfirmationComplete = value != 0
					default:
						t.Fatalf("oracle case %q names unknown field %q", w.Name, field)
					}
				}
			}
			if got := policy.Converged(input); got != w.Converged {
				t.Errorf("weakened %q converges = %v, want %v", w.Name, got, w.Converged)
			}
		})
	}
}

// TestConvergenceAcceptanceMutationProvesTheGateIsLive weakens the all-clear
// input's obligation the oracle asserts and requires refusal: removing the
// ledger-current obligation must not converge, so a production change that
// drops that guard turns this red.
func TestConvergenceAcceptanceMutationProvesTheGateIsLive(t *testing.T) {
	oracle := loadConvergenceAcceptance(t)
	input := acceptanceInput(oracle)
	if !policy.Converged(input) {
		t.Fatal("the all-clear input does not converge before any mutation")
	}
	// The mutation: drop the ledger-current obligation while keeping every
	// other field at its all-clear value. The oracle's stale-ledger case
	// pins this refusal; production converging here voids the gate.
	input.LedgerCurrent = false
	if policy.Converged(input) {
		t.Fatal("a stale ledger still converges; the suite did not turn red")
	}
	// The red proof is the comparison against the literal: the oracle still
	// records ledger_current true with converged true, so the mutated input
	// differs from the expected one in exactly the obligation removed.
	if !oracle.AllClear.LedgerCurrent || !oracle.AllClear.Converged {
		t.Fatalf("oracle all-clear = ledger %v converged %v, want true/true",
			oracle.AllClear.LedgerCurrent, oracle.AllClear.Converged)
	}
}

func asFloat(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
