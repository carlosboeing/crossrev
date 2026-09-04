package config_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
)

func TestEndpointResolvesToItsPair(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nendpoints:\n  ollama:\n    base_url: http://127.0.0.1:11434\n    token_env: ANTHROPIC_AUTH_TOKEN\n"}}
	endpoint, err := mustLoad(t, core.Revision{}, tree).Endpoint("ollama")
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if got, want := endpoint.String(), "http://127.0.0.1:11434 ANTHROPIC_AUTH_TOKEN"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// An unresolved name is a hard failure, never a fallback. Falling back to the
// vendor's own API would mean running one model while the config names another
// — the same silent substitution the divergence guard exists to catch, arriving
// through a different door (lib/config.sh:344-348).
func TestAnUndefinedEndpointIsRefusedRatherThanFallenBackFrom(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\n"}}
	_, err := mustLoad(t, core.Revision{}, tree).Endpoint("ollama")
	refusal, ok := err.(*config.Refusal)
	if !ok {
		t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
	}
	if want := "the endpoint 'ollama' is named in the config but defined nowhere"; refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
	if !strings.Contains(refusal.Hint, config.OperatorPath()) {
		t.Errorf("the hint does not name the operator file: %q", refusal.Hint)
	}
	if !strings.Contains(refusal.Hint, "will not silently fall back") {
		t.Errorf("the hint does not say it will not fall back: %q", refusal.Hint)
	}
}

// No endpoint named is not a failure: most configurations name none, and the
// Bash cfg_endpoint returns 1 without a message for it (lib/config.sh:351).
func TestNoEndpointNamedIsNotARefusal(t *testing.T) {
	loaded := mustLoad(t, core.Revision{}, files{"": {}})
	for _, name := range []string{"", "null"} {
		// A sentinel matched with errors.Is, so a caller that wraps it still
		// reads it as the same condition.
		if err := fmt.Errorf("resolve the reviewer endpoint: %w", mustEndpointError(t, loaded, name)); !errors.Is(err, config.ErrNoEndpointNamed) {
			t.Errorf("Endpoint(%q) returned %v, want ErrNoEndpointNamed", name, err)
		}
	}
	if got := config.ErrNoEndpointNamed.Error(); got != "no endpoint is named" {
		t.Errorf("Error() = %q", got)
	}
}

func mustEndpointError(t *testing.T, loaded *config.Config, name string) error {
	t.Helper()
	_, err := loaded.Endpoint(name)
	if err == nil {
		t.Fatalf("Endpoint(%q) returned no error", name)
	}
	return err
}

// An endpoint defined as a scalar is malformed, and the Bash reaches the
// missing-base_url refusal for it rather than the defined-nowhere one:
// `.endpoints[$n] // empty` is non-empty for a string, and the base_url read
// off that string comes back empty (lib/config.sh:353-359).
func TestAScalarEndpointHasNoBaseURL(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nendpoints:\n  ollama: \"http://x\"\n"}}
	_, err := mustLoad(t, core.Revision{}, tree).Endpoint("ollama")
	refusal, ok := err.(*config.Refusal)
	if !ok {
		t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
	}
	if want := "the endpoint 'ollama' has no base_url"; refusal.Message != want {
		t.Errorf("message = %q, want %q", refusal.Message, want)
	}
}

// A definition that is null or false is empty to jq's alternative operator, so
// it reads as no definition at all.
func TestANullEndpointIsDefinedNowhere(t *testing.T) {
	for _, document := range []string{
		"version: 1\nendpoints:\n  ollama: null\n",
		"version: 1\nendpoints:\n  ollama: false\n",
	} {
		tree := files{"": {".github/crossrev.yml": document}}
		_, err := mustLoad(t, core.Revision{}, tree).Endpoint("ollama")
		refusal, ok := err.(*config.Refusal)
		if !ok {
			t.Fatalf("expected a *config.Refusal, got %T: %v", err, err)
		}
		if want := "the endpoint 'ollama' is named in the config but defined nowhere"; refusal.Message != want {
			t.Errorf("message = %q, want %q", refusal.Message, want)
		}
	}
}

// An endpoint defined as an empty mapping is present to jq, so it reaches the
// missing-base_url refusal rather than the defined-nowhere one.
func TestAnEmptyEndpointHasNoBaseURL(t *testing.T) {
	tree := files{"": {".github/crossrev.yml": "version: 1\nendpoints:\n  ollama: {}\n"}}
	_, err := mustLoad(t, core.Revision{}, tree).Endpoint("ollama")
	if err == nil || err.Error() != "the endpoint 'ollama' has no base_url" {
		t.Errorf("error = %v, want the missing base_url refusal", err)
	}
}

// An endpoint defined in the operator file alone still resolves: that is the
// whole point of the second layer, for a URL meaningless on a runner.
func TestAnOperatorOnlyEndpointResolves(t *testing.T) {
	operatorPath := config.OperatorPath()
	tree := files{"": {operatorPath: "version: 1\nendpoints:\n  mine:\n    base_url: http://home.local/\n    token_env: HOME_TOKEN\n"}}
	endpoint, err := mustLoad(t, core.Revision{}, tree).Endpoint("mine")
	if err != nil {
		t.Fatalf("Endpoint: %v", err)
	}
	if endpoint.BaseURL != "http://home.local/" {
		t.Errorf("base_url = %q", endpoint.BaseURL)
	}
}
