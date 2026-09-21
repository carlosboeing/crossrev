package prstate_test

import (
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/prstate"
)

func TestFitMarkerComment(t *testing.T) {
	if err := prstate.FitMarkerComment(""); err != nil {
		t.Fatalf("empty comment refused: %v", err)
	}
	exact := strings.Repeat("x", prstate.CommentCap)
	if err := prstate.FitMarkerComment(exact); err != nil {
		t.Fatalf("comment at cap refused: %v", err)
	}
	over := strings.Repeat("x", prstate.CommentCap+1)
	if err := prstate.FitMarkerComment(over); err == nil {
		t.Fatal("comment over cap accepted")
	}
}
