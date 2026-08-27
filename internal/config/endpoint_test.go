package config_test

import (
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
// through a different door (lib/config.sh:308-312).
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
// Bash cfg_endpoint returns 1 without a message for it (lib/config.sh:315).
func TestNoEndpointNamedIsNotARefusal(t *testing.T) {
	loaded := mustLoad(t, core.Revision{}, files{"": {}})
	for _, name := range []string{"", "null"} {
		_, err := loaded.Endpoint(name)
		if _, ok := err.(config.ErrNoEndpointNamed); !ok {
			t.Errorf("Endpoint(%q) returned %T, want ErrNoEndpointNamed", name, err)
		}
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
