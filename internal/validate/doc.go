// Package validate checks a leg's payload the way lib/validate.sh checks it:
// structural shape for both legs, and a semantic half for the resolve leg that
// compares the answer against what the orchestrator itself supplied.
//
// # What this package does not decide
//
// The retry budget is not here, and that is deliberate rather than an
// oversight. lib/run.sh:804-805 spends it: a shape failure gets one attempt, or
// two when `validate_harness_is_schema_native` (lib/validate.sh:141-143) says
// the harness does not constrain its own output, and a semantic failure gets
// one more. The budget belongs with the leg that spends it, and no leg is
// ported yet, so nothing here reads a harness descriptor. What this package
// supplies is the attribution the budget is keyed on: ShapeError.Code is the
// shell's exit 1 and SemanticError.Code is its exit 2.
//
// # Where a message can still differ from jq's
//
// A number reaches a message as the literal the payload wrote it as, because
// that is what jq prints. jq itself rewrites one form: an exponent comes back
// in General Decimal Arithmetic's to-scientific-string spelling, so `1e2` is
// printed `1E+2`. In the shipped pipeline that never shows, because
// lib/run.sh:865 pipes the payload through `jq -c '.payload'` before the
// validator sees it and every exponent is already normalised. An all-Go
// pipeline has no jq at that boundary, so a message here can carry `1e2` where
// the shell's carries `1E+2`. ADR 0019 records the difference; only the
// message text is affected, never the accept-or-refuse answer.
package validate
