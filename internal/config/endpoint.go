package config

import "fmt"

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
// against its own vendor.
type ErrNoEndpointNamed struct{}

// Error names the condition.
func (ErrNoEndpointNamed) Error() string { return "no endpoint is named" }

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
		return Endpoint{}, ErrNoEndpointNamed{}
	}
	defined := c.Merged.Object("endpoints").Object(name)
	if defined == nil {
		return Endpoint{}, &Refusal{
			Message: fmt.Sprintf("the endpoint '%s' is named in the config but defined nowhere", name),
			Hint:    fmt.Sprintf("Define it under endpoints: in the repository config, or in %s if it is machine-local. crossrev will not silently fall back to the vendor's own API.", OperatorPath()),
		}
	}
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
