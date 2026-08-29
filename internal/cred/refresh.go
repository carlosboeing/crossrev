// refresh.go — the single writer.
//
// Only the refresher workflow reaches this. Everything about it is derived from
// the credential rather than hardcoded: the issuer and the client id come out
// of the access token's own claims, and the token endpoint comes from the
// issuer's OpenID discovery document. That is not fastidiousness — the endpoint
// is not where the obvious guess puts it, so a hardcoded URL would have shipped
// broken (lib/credentials.sh:229-234).
//
// Refresh returns the new credential's bytes and writes nothing. lib/auth.sh
// is what puts them somewhere: :1020 re-reads the expiry out of them and
// refuses to write back one it cannot read, and :1034-1042 hands them to
// `gh secret set`. Keeping the write out of here is what makes "a leg restores,
// reads and discards" a property of the type rather than of the caller.
//
// # This is orchestrator work
//
// The HTTP calls below go to the harness vendor's own OAuth endpoints on
// crossrev's behalf. No model-facing process is started, no forge credential is
// involved, and nothing here builds an exec.Spec — so the audience opt-out that
// internal/archtest confines to internal/exec, internal/forge/ghexec and
// internal/vcs is not needed and not taken.
//
// The client is injected for the same reason the environment is: a test must
// be able to exercise every branch without a vendor and without a network.

package cred

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPClient is the one thing Refresh needs from net/http.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// The two curl timeouts, kept as the shell writes them: `--max-time 20` on the
// discovery read (lib/credentials.sh:238) and `--max-time 60` on the token
// exchange (lib/credentials.sh:282).
const (
	discoveryTimeout = 20 * time.Second
	tokenTimeout     = 60 * time.Second
)

// refreshScope is the scope the token request asks for
// (lib/credentials.sh:281).
const refreshScope = "openid profile email offline_access"

// maxResponseBytes bounds what a vendor response may be read into.
//
// curl has no such limit and neither does the shell, so this has no
// counterpart there. It is here because the whole body is held in memory to be
// parsed, and a refresher job is unattended: a response that never ends would
// otherwise grow until the runner died, with the failure looking nothing like
// its cause. A megabyte is three orders of magnitude more than any token
// response measured.
const maxResponseBytes = 1 << 20

// RefreshOptions are Refresh's dependencies. Every zero value is the real one.
type RefreshOptions struct {
	// HTTP is the client both requests go through. Nil is http.DefaultClient.
	HTTP HTTPClient
	// Now stamps `.last_refresh`. Nil is time.Now.
	Now func() time.Time
}

func (o RefreshOptions) client() HTTPClient {
	if o.HTTP != nil {
		return o.HTTP
	}
	return http.DefaultClient
}

func (o RefreshOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Refresh exchanges a stored credential's refresh token for a new credential,
// and returns the new one: cred_refresh (lib/credentials.sh:253-308).
//
// It returns nothing on any failure, so a caller cannot mistake a half-answer
// for a credential and write it back over the good one
// (lib/credentials.sh:244-245). Every failure names its reason in the error
// rather than on stderr — which is what the shell's `>&2` redirections were
// working around, since its stdout is the return value
// (lib/credentials.sh:247-252).
func Refresh(ctx context.Context, d Descriptor, credential []byte, o RefreshOptions) ([]byte, error) {
	claims, err := TokenClaims(d, credential)
	if err != nil {
		return nil, &Refusal{
			Kind:   ErrRefresh,
			Err:    err,
			Reason: "the stored credential has no readable access token",
			Action: "Re-seed the secret by hand from a fresh login.",
		}
	}

	stored, err := decodeObject(credential)
	if err != nil {
		return nil, &Refusal{
			Kind:   ErrRefresh,
			Err:    err,
			Reason: "the stored credential is not a JSON object",
			Action: "Re-seed the secret by hand from a fresh login.",
		}
	}
	refreshToken := stored.stringAt("tokens", "refresh_token")

	if claims.Issuer == "" || claims.ClientID == "" || refreshToken == "" {
		return nil, &Refusal{
			Kind:   ErrRefresh,
			Reason: "the stored credential is missing an issuer, a client id or a refresh token",
			Action: "Re-seed the secret by hand from a fresh login.",
		}
	}

	endpoint, err := discoverTokenEndpoint(ctx, claims.Issuer, o)
	if err != nil {
		return nil, &Refusal{
			Kind:   ErrRefresh,
			Err:    err,
			Reason: fmt.Sprintf("could not read a token endpoint from %s's discovery document", claims.Issuer),
			Action: "The stored secret is untouched. Check that the runner can reach the vendor.",
		}
	}

	response, err := exchange(ctx, endpoint, claims.ClientID, refreshToken, o)
	if err != nil {
		return nil, err
	}

	newAccess := response.stringAt("access_token")
	if newAccess == "" {
		return nil, &Refusal{
			Kind:   ErrRefresh,
			Reason: "the vendor's response carried no access token",
			Action: "The stored secret is untouched. Re-seed it by hand if this repeats.",
		}
	}
	// A response that returns no replacement refresh token means this one was
	// not consumed, so keeping it is correct rather than a fallback
	// (lib/credentials.sh:298-300).
	newRefresh := response.stringAt("refresh_token")
	if newRefresh == "" {
		newRefresh = refreshToken
	}
	newID := response.stringAt("id_token")
	if newID == "" {
		newID = stored.stringAt("tokens", "id_token")
	}

	// The four assignments of lib/credentials.sh:303-308, in that order, over
	// the stored credential rather than a document built here — so every key
	// the vendor's store carries and CrossRev knows nothing about survives.
	if err := stored.setString([]string{"tokens", "access_token"}, newAccess); err != nil {
		return nil, err
	}
	if err := stored.setString([]string{"tokens", "refresh_token"}, newRefresh); err != nil {
		return nil, err
	}
	if err := stored.setString([]string{"tokens", "id_token"}, newID); err != nil {
		return nil, err
	}
	if err := stored.setString([]string{"last_refresh"}, o.now().UTC().Format("2006-01-02T15:04:05Z")); err != nil {
		return nil, err
	}
	return stored.marshal()
}

// discoverTokenEndpoint reads `.token_endpoint` out of the issuer's OpenID
// discovery document: _cred_discovery_token_endpoint (lib/credentials.sh:236-240).
func discoverTokenEndpoint(ctx context.Context, issuer string, o RefreshOptions) (string, error) {
	url := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"

	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// `curl -fsS` fails on a non-2xx and prints nothing, which is what makes
	// the shell's `[[ -n "$endpoint" ]]` check catch a 404 as well as a
	// document with no token_endpoint (lib/credentials.sh:238, :274).
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("the discovery document answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", err
	}
	document, err := decodeObject(body)
	if err != nil {
		return "", err
	}
	endpoint := document.stringAt("token_endpoint")
	if endpoint == "" {
		return "", fmt.Errorf("the discovery document names no token_endpoint")
	}
	return endpoint, nil
}

// exchange posts the refresh grant and returns the vendor's answer.
func exchange(ctx context.Context, endpoint, clientID, refreshToken string, o RefreshOptions) (*object, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     clientID,
		"refresh_token": refreshToken,
		"scope":         refreshScope,
	})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &Refusal{Kind: ErrRefresh, Err: err,
			Reason: fmt.Sprintf("could not build the refresh request for %s", endpoint),
			Action: "The stored secret is untouched."}
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client().Do(req)
	if err != nil {
		return nil, &Refusal{Kind: ErrRefresh, Err: err,
			Reason: fmt.Sprintf("could not reach %s at all", endpoint),
			Action: "The stored secret is untouched. Check that the runner can reach the vendor."}
	}
	defer resp.Body.Close()

	// Read the body whatever the status. A rejection comes back as JSON naming
	// the reason, and `token_expired` and `invalid_client` need different
	// fixes: the difference is only in there, which is why the shell does not
	// pass `-f` to curl (lib/credentials.sh:276-278).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, &Refusal{Kind: ErrRefresh, Err: err,
			Reason: fmt.Sprintf("the vendor's answer from %s could not be read", endpoint),
			Action: "The stored secret is untouched."}
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &Refusal{
			Kind:   ErrRefresh,
			Reason: fmt.Sprintf("the vendor rejected the refresh (HTTP %d): %s", resp.StatusCode, rejectionReason(raw)),
			Action: "The stored secret is untouched, so the chain still holds until it expires. Re-seed it by hand if this keeps failing.",
		}
	}

	answer, err := decodeObject(raw)
	if err != nil {
		return nil, &Refusal{Kind: ErrRefresh, Err: err,
			Reason: "the vendor answered, but not with a JSON object",
			Action: "The stored secret is untouched. Re-seed it by hand if this repeats."}
	}
	return answer, nil
}

// rejectionReason is what the vendor said, or "no reason given".
//
// The shell writes this as one jq filter,
// `.error.message // .error_description // .error // "no reason given"`
// (lib/credentials.sh:288), and that filter cannot reach its own third arm. A
// body of `{"error":"invalid_client"}` — the shape RFC 6749 section 5.2
// specifies, and the common one — makes `.error.message` a hard jq error rather
// than null, so jq exits 5, prints nothing, and the message reads
// `the vendor rejected the refresh (HTTP 400): ` with the reason missing.
// Measured:
//
//	echo '{"error":{"message":"m"}}'   -> m
//	echo '{"error_description":"d"}'   -> d
//	echo '{"error":"e"}'               -> "", jq exit 5
//	echo '{}'                          -> no reason given
//
// The three arms below are the filter's intent, with the third reached. That is
// a deliberate divergence: the whole reason the shell reads the body at all is
// the sentence above the filter, saying token_expired and invalid_client need
// different fixes and the difference is only in there.
func rejectionReason(raw []byte) string {
	document, err := decodeObject(raw)
	if err != nil {
		return "no reason given"
	}
	if nested, err := decodeObject(document.values["error"]); err == nil {
		if message := nested.stringAt("message"); message != "" {
			return message
		}
	}
	if description := document.stringAt("error_description"); description != "" {
		return description
	}
	if reason := document.stringAt("error"); reason != "" {
		return reason
	}
	return "no reason given"
}

// ---------------------------------------------------------------------------
// An object that remembers its key order
// ---------------------------------------------------------------------------
//
// The refreshed credential is written by `jq -c` over the stored one
// (lib/credentials.sh:303-308), and jq keeps a document's own key order,
// appending a key it had to create. encoding/json cannot: a map marshals in
// sorted order, so `{"OPENAI_API_KEY":…,"auth_mode":…}` would come back
// reordered and every key the store carries would move.
//
// Nothing downstream reads that order — the bytes go to `gh secret set` and
// then to a vendor CLI — but the value is a credential an operator may compare
// against the one it replaced, and a wholesale reordering is the kind of diff
// that hides the one line that mattered. It also keeps numbers as they arrived:
// decoding into `any` would turn an account id into a float64 and write it back
// in exponent form.

// object is a JSON object with its keys in the order they were read.
type object struct {
	keys   []string
	values map[string]json.RawMessage
}

func decodeObject(raw []byte) (*object, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("the value is empty")
	}
	o := &object{values: map[string]json.RawMessage{}}
	if err := json.Unmarshal(raw, o); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *object) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := opening.(json.Delim); !ok || delim != '{' {
		return fmt.Errorf("the value is not a JSON object")
	}

	o.keys = nil
	o.values = map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok {
			return fmt.Errorf("a JSON object key is not a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if _, seen := o.values[name]; !seen {
			o.keys = append(o.keys, name)
		}
		// A duplicate key keeps the last value and its first position, which is
		// what jq does with one (lib/harnesses.sh:11-15 records the measurement
		// for the same reason).
		o.values[name] = value
	}
	// Consumes the closing brace, and reports trailing content after it.
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if decoder.More() {
		return fmt.Errorf("the value carries more than one JSON document")
	}
	return nil
}

// stringAt reads a string down a path of object keys, answering "" for a key
// that is absent, null, or not a string. It is `jq -r '.a.b // empty'`.
func (o *object) stringAt(path ...string) string {
	current := o
	for depth, key := range path {
		value, present := current.values[key]
		if !present {
			return ""
		}
		if depth == len(path)-1 {
			return stringClaim(value)
		}
		next, err := decodeObject(value)
		if err != nil {
			return ""
		}
		current = next
	}
	return ""
}

// setString writes a string down a path of object keys, creating any object on
// the way that is missing. `jq '.tokens.access_token = $a'` on `{}` produces
// `{"tokens":{"access_token":…}}`, measured, and this does the same.
func (o *object) setString(path []string, value string) error {
	encoded, err := encodeString(value)
	if err != nil {
		return err
	}
	return o.setRaw(path, encoded)
}

func (o *object) setRaw(path []string, value json.RawMessage) error {
	key := path[0]
	if len(path) == 1 {
		if _, present := o.values[key]; !present {
			o.keys = append(o.keys, key)
		}
		o.values[key] = value
		return nil
	}

	child := &object{values: map[string]json.RawMessage{}}
	if existing, present := o.values[key]; present {
		decoded, err := decodeObject(existing)
		if err != nil {
			// jq replaces a non-object with an object here rather than failing.
			// Refusing instead: a credential whose `.tokens` is not an object
			// is one this build does not understand, and overwriting it would
			// discard whatever it held.
			return fmt.Errorf("%w: the credential's %q is not an object", ErrRefresh, key)
		}
		child = decoded
	} else {
		o.keys = append(o.keys, key)
	}
	if err := child.setRaw(path[1:], value); err != nil {
		return err
	}
	nested, err := child.marshal()
	if err != nil {
		return err
	}
	o.values[key] = nested
	return nil
}

// marshal renders the object compactly, in key order.
func (o *object) marshal() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for at, key := range o.keys {
		if at > 0 {
			out.WriteByte(',')
		}
		encoded, err := encodeString(key)
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
		out.WriteByte(':')
		// Compact rather than re-encode: an untouched value keeps its own
		// literal, so a large integer stays an integer instead of being read
		// into a float64 and written back in exponent form.
		if err := json.Compact(&out, o.values[key]); err != nil {
			return nil, err
		}
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// encodeString renders one JSON string the way jq does.
//
// SetEscapeHTML(false) is the whole point: encoding/json turns `<`, `>` and `&`
// into <, > and & by default, and jq leaves them alone. Measured:
// `jq -c .` on `{"x":"a<b>&c"}` prints it back unchanged.
func encodeString(value string) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	// Encode appends a newline.
	return bytes.TrimRight(out.Bytes(), "\n"), nil
}
