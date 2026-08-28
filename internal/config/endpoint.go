package config

import (
	"errors"
	"fmt"
)

// Endpoint is a named endpoint resolved out of the merge.
type Endpoint struct {
	Name     string
	BaseURL  string
	TokenEnv string
}

// String is the "<base_url> <token_env>" pair cfg_endpoint prints at
// lib/config.sh:325.
func (e Endpoint) String() string { return e.BaseURL + " " + e.TokenEnv }

// ErrNoEndpointNamed reports that nothing named an endpoint, which is not a
// failure: most configurations name none, and the caller then runs the harness
// against its own vendor. A sentinel rather than a type, so a caller that wraps
// it can still match it with errors.Is.
var ErrNoEndpointNamed = errors.New("no endpoint is named")

// Endpoint resolves a named endpoint to its base URL and token variable.
//
// An unresolved name is a hard failure, never a fallback. If `endpoint: ollama`
// does not resolve because the run is on a runner that cannot see it, the leg
// stops and says so. Falling back to Anthropic would mean running Claude while
// the config says Ollama — the same silent substitution the divergence guard
// exists to catch, arriving through a different door (lib/config.sh:306-326).
//
// An empty name, or the literal "null" that jq prints for an unset key, is not
// an endpoint at all: cfg_endpoint returns 1 without a message for it
// (lib/config.sh:315), so this returns ErrNoEndpointNamed rather than a
// refusal.
func (c *Config) Endpoint(name string) (Endpoint, error) {
	if name == "" || name == "null" {
		return Endpoint{}, ErrNoEndpointNamed
	}
	// `defined nowhere` is the answer to `.endpoints[$n] // empty` coming back
	// empty at lib/config.sh:317, so it covers an absent key and a key holding
	// null or false, and nothing else. A definition that is present and not a
	// mapping — `ollama: "http://x"` — is not empty there: the Bash reads
	// base_url off a string, gets nothing, and refuses for the missing
	// base_url instead (lib/config.sh:321-323). Object(name) is nil for that
	// definition, and Value on a nil object reads every field as absent, so
	// the same refusal is reached here.
	value := c.Merged.Object("endpoints").Value(name)
	if boolean, isBoolean := value.(bool); value == nil || (isBoolean && !boolean) {
		return Endpoint{}, &Refusal{
			Message: fmt.Sprintf("the endpoint '%s' is named in the config but defined nowhere", name),
			Hint:    fmt.Sprintf("Define it under endpoints: in the repository config, or in %s if it is machine-local. crossrev will not silently fall back to the vendor's own API.", OperatorPath()),
		}
	}
	defined, _ := value.(*Object)
	baseURL := alternative(defined.Value("base_url"))
	tokenEnv := alternative(defined.Value("token_env"))
	if baseURL == "" {
		return Endpoint{}, &Refusal{
			Message: fmt.Sprintf("the endpoint '%s' has no base_url", name),
			Hint:    "Add base_url: to its definition.",
		}
	}
	if tokenEnv == "" {
		return Endpoint{}, &Refusal{
			Message: fmt.Sprintf("the endpoint '%s' has no token_env", name),
			Hint:    "Add token_env: naming the environment variable that carries its token. Ollama's docs use ANTHROPIC_AUTH_TOKEN where Kimi's use ANTHROPIC_API_KEY, which is why the name is not assumed.",
		}
	}
	return Endpoint{Name: name, BaseURL: baseURL, TokenEnv: tokenEnv}, nil
}
