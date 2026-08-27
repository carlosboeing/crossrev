package core

import (
	"errors"
	"fmt"
)

// findingIDLength is the width of a finding's identity. lib/state.sh:242 hashes
// the identity bytes with sha256 and cuts the digest to its first 16
// characters, which are lowercase hexadecimal.
const findingIDLength = 16

// FindingID is a 16-character hexadecimal fingerprint identifying a finding
// across passes.
//
// The type is declared here so every tier can name one. Only internal/prstate
// may mint one, and an architecture test fails on a conversion to this type
// anywhere else: an id derived by a second implementation would look like a new
// finding on the next pass and get posted again.
type FindingID string

// Valid reports whether the id has the shape lib/state.sh:242 produces.
func (f FindingID) Valid() bool { return isHex(string(f), findingIDLength) }

// String renders the id as the marker holds it.
func (f FindingID) String() string { return string(f) }

// Severity is how bad a finding is, and nothing else. The threshold that
// decides what the resolve leg may change code for is a separate policy value.
type Severity string

// The three severities, from the `severity` enum in
// schemas/findings.schema.json.
const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
)

// ErrSeverity is returned for a severity outside the schema's enum.
var ErrSeverity = errors.New("a severity is high, medium or low")

// Severities lists the three severities, worst first.
func Severities() []Severity { return []Severity{SeverityHigh, SeverityMedium, SeverityLow} }

// ParseSeverity accepts only the schema's three values.
func ParseSeverity(s string) (Severity, error) {
	switch Severity(s) {
	case SeverityHigh:
		return SeverityHigh, nil
	case SeverityMedium:
		return SeverityMedium, nil
	case SeverityLow:
		return SeverityLow, nil
	}
	return "", fmt.Errorf("%w: %q", ErrSeverity, s)
}

// String renders the severity as the schema spells it.
func (s Severity) String() string { return string(s) }

// Category is what kind of defect a finding is.
type Category string

// The six categories, from the `category` enum in
// schemas/findings.schema.json. The set is closed on purpose: free text turns
// the summary table to mush.
const (
	CategoryCorrectness     Category = "correctness"
	CategorySecurity        Category = "security"
	CategoryPerformance     Category = "performance"
	CategoryMaintainability Category = "maintainability"
	CategoryTesting         Category = "testing"
	CategoryDocs            Category = "docs"
)

// ErrCategory is returned for a category outside the schema's enum.
var ErrCategory = errors.New("a category is not one the findings schema lists")

// Categories lists the six categories in schema order.
func Categories() []Category {
	return []Category{
		CategoryCorrectness,
		CategorySecurity,
		CategoryPerformance,
		CategoryMaintainability,
		CategoryTesting,
		CategoryDocs,
	}
}

// ParseCategory accepts only the schema's six values.
func ParseCategory(s string) (Category, error) {
	for _, c := range Categories() {
		if Category(s) == c {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrCategory, s)
}

// String renders the category as the schema spells it.
func (c Category) String() string { return string(c) }

// Side is which side of the diff a finding anchors to.
type Side string

// The two sides, from the `side` enum in schemas/findings.schema.json. GitHub's
// review API takes them uppercase, so the value is uppercase here too.
const (
	SideLeft  Side = "LEFT"
	SideRight Side = "RIGHT"
)

// ErrSide is returned for a side outside the schema's enum.
var ErrSide = errors.New("a side is LEFT or RIGHT")

// Sides lists both sides.
func Sides() []Side { return []Side{SideLeft, SideRight} }

// ParseSide accepts only the schema's two values, case included.
func ParseSide(s string) (Side, error) {
	switch Side(s) {
	case SideLeft:
		return SideLeft, nil
	case SideRight:
		return SideRight, nil
	}
	return "", fmt.Errorf("%w: %q", ErrSide, s)
}

// String renders the side as the schema spells it.
func (s Side) String() string { return string(s) }

// PriorStatus is how the review leg classified a finding carried in from an
// earlier pass.
type PriorStatus string

// The four statuses, from the `prior[].status` enum in
// schemas/findings.schema.json.
const (
	PriorAddressed        PriorStatus = "addressed"
	PriorCrediblyDisputed PriorStatus = "credibly-disputed"
	PriorStillOpen        PriorStatus = "still-open"
	PriorRegressed        PriorStatus = "regressed"
)

// ErrPriorStatus is returned for a status outside the schema's enum.
var ErrPriorStatus = errors.New("a prior status is not one the findings schema lists")

// PriorStatuses lists the four statuses in schema order.
func PriorStatuses() []PriorStatus {
	return []PriorStatus{PriorAddressed, PriorCrediblyDisputed, PriorStillOpen, PriorRegressed}
}

// ParsePriorStatus accepts only the schema's four values.
func ParsePriorStatus(s string) (PriorStatus, error) {
	for _, p := range PriorStatuses() {
		if PriorStatus(s) == p {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrPriorStatus, s)
}

// String renders the status as the schema spells it.
func (p PriorStatus) String() string { return string(p) }
