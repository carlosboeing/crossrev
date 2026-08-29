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
	"net/url"
	"strings"
	"time"
)

// HTTPClient is the one thing Refresh needs from net/http.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// The two curl timeouts, kept as the shell writes them: `--max-time 20` on the
// discovery read (lib/credentials.sh:238) and `--max-time 60` on the token
// exchange (lib/credentials.sh:299).
const (
	discoveryTimeout = 20 * time.Second
	tokenTimeout     = 60 * time.Second
)

// refreshScope is the scope the token request asks for
// (lib/credentials.sh:298).
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

// noRedirectClient is the real client, and it stops at the first answer.
//
// http.DefaultClient follows up to ten redirects, and a 307 or 308 replays the
// request body — so a token endpoint that answered `308 Location: attacker`
// would receive the whole refresh grant a second time, refresh token included,
// at a host the vendor's discovery document did not name. The redirect was
// measured doing exactly that, with Refresh returning success on the last hop's
// answer: the caller then writes a credential back believing the chain is
// intact while a third party holds the token.
//
// http.ErrUseLastResponse is what `curl` without `-L` does — the shell passes
// `curl -sS` with no `-L` at lib/credentials.sh:299, so this is parity as well
// as the fix. The redirect arrives as the vendor's final answer and the 2xx
// check below refuses it.
//
// A caller that injects its own HTTPClient decides its own redirect policy,
// which is the point of injecting one.
var noRedirectClient = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (o RefreshOptions) client() HTTPClient {
	if o.HTTP != nil {
		return o.HTTP
	}
	return noRedirectClient
}

func (o RefreshOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Refresh exchanges a stored credential's refresh token for a new credential,
// and returns the new one: cred_refresh (lib/credentials.sh:270-326).
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

	// The refusal is built where the cause is known rather than flattened here.
	// The shell prints one sentence for every discovery failure — "could not
	// read a token endpoint from $issuer's discovery document"
	// (lib/credentials.sh:291) — and it reads as a vendor fault whichever one
	// happened, including the two that are not: a document larger than CrossRev
	// reads, and one naming an http endpoint. That is a deliberate divergence.
	endpoint, err := discoverTokenEndpoint(ctx, claims.Issuer, o)
	if err != nil {
		return nil, err
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
	// (lib/credentials.sh:315-317).
	newRefresh := response.stringAt("refresh_token")
	if newRefresh == "" {
		newRefresh = refreshToken
	}
	newID := response.stringAt("id_token")
	if newID == "" {
		newID = stored.stringAt("tokens", "id_token")
	}

	// The four assignments of lib/credentials.sh:320-325, in that order, over
	// the stored credential rather than a document built here — so every key
	// the vendor's store carries and CrossRev knows nothing about survives.
	stored.setString([]string{"tokens", "access_token"}, newAccess)
	stored.setString([]string{"tokens", "refresh_token"}, newRefresh)
	stored.setString([]string{"tokens", "id_token"}, newID)
	// .UTC() rather than the clock's own zone: `date -u` at
	// lib/credentials.sh:321 stamps UTC whatever TZ the runner carries, and the
	// trailing Z in the layout would otherwise be a lie about a local time.
	stored.setString([]string{"last_refresh"}, o.now().UTC().Format("2006-01-02T15:04:05Z"))
	return stored.marshal(), nil
}

// discoverTokenEndpoint reads `.token_endpoint` out of the issuer's OpenID
// discovery document: _cred_discovery_token_endpoint (lib/credentials.sh:236-240).
//
// Every failure returns its own *Refusal naming its own cause, rather than one
// sentence covering all of them.
//
// # What is checked about the endpoint, and what is not
//
// An https issuer may not name an http token endpoint. The refresh token is in
// the request body, so a cleartext POST hands it to anybody on the path, and no
// vendor serves one — a downgrade here is either a compromised discovery
// document or a misconfiguration, and both are refusals rather than requests to
// make. The shell checks neither and would post to it.
//
// The endpoint's HOST is deliberately not required to match the issuer's. A
// vendor is entitled to serve its token endpoint from a different name, and
// refusing that would break a working refresh to close a hole the issuer
// already controls: the document is read from the issuer over TLS, so a host an
// attacker chose means the issuer is already theirs.
func discoverTokenEndpoint(ctx context.Context, issuer string, o RefreshOptions) (string, error) {
	refuse := func(err error, reason, action string) (string, error) {
		return "", &Refusal{Kind: ErrRefresh, Err: err,
			Reason: fmt.Sprintf("%s's discovery document %s", issuer, reason),
			Action: action}
	}
	const untouched = "The stored secret is untouched."
	const reachable = "The stored secret is untouched. Check that the runner can reach the vendor."

	address := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"

	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return refuse(err, "could not be asked for", untouched)
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return refuse(err, "could not be reached at all", reachable)
	}
	defer resp.Body.Close()

	// `curl -fsS` fails on a non-2xx and prints nothing, which is what makes
	// the shell's `[[ -n "$endpoint" ]]` check catch a 404 as well as a
	// document with no token_endpoint (lib/credentials.sh:238, :291).
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return refuse(nil, fmt.Sprintf("answered HTTP %d", resp.StatusCode), reachable)
	}
	body, oversize, err := readCapped(resp.Body)
	if err != nil {
		return refuse(err, "could not be read", untouched)
	}
	if oversize {
		// Reported as its own cause rather than as a parse failure. A truncated
		// document is not a document the vendor got wrong.
		return refuse(nil, fmt.Sprintf("is larger than the %d bytes CrossRev reads", maxResponseBytes), untouched)
	}
	document, err := decodeObject(body)
	if err != nil {
		return refuse(err, "is not a JSON object", untouched)
	}
	endpoint := document.stringAt("token_endpoint")
	if endpoint == "" {
		return refuse(nil, "names no token_endpoint", untouched)
	}
	if err := refuseADowngrade(issuer, endpoint); err != nil {
		return refuse(err, "names a token endpoint CrossRev will not post to",
			"The stored secret is untouched. A refresh token in a cleartext request body is readable by anything on the path.")
	}
	return endpoint, nil
}

// refuseADowngrade refuses an http token endpoint named by an https issuer.
func refuseADowngrade(issuer, endpoint string) error {
	from, err := url.Parse(issuer)
	if err != nil {
		return err
	}
	to, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if from.Scheme == "https" && to.Scheme != "https" {
		return fmt.Errorf("an https issuer named a %q token endpoint", to.Scheme)
	}
	return nil
}

// readCapped reads a vendor response, and says whether it ran past the cap.
//
// One byte over the limit is read so that "as long as CrossRev reads" and
// "longer than CrossRev reads" are told apart. Without it a body sitting
// exactly on the cap and one ten times it are the same bytes, and the second is
// reported as whatever the truncation failed to parse as.
func readCapped(body io.Reader) (raw []byte, oversize bool, err error) {
	raw, err = io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > maxResponseBytes {
		return nil, true, nil
	}
	return raw, false, nil
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
	// pass `-f` to curl (lib/credentials.sh:293-295).
	raw, oversize, err := readCapped(resp.Body)
	if err != nil {
		return nil, &Refusal{Kind: ErrRefresh, Err: err,
			Reason: fmt.Sprintf("the vendor's answer from %s could not be read", endpoint),
			Action: "The stored secret is untouched."}
	}
	if oversize {
		return nil, &Refusal{Kind: ErrRefresh,
			Reason: fmt.Sprintf("the vendor's answer from %s is larger than the %d bytes CrossRev reads", endpoint, maxResponseBytes),
			Action: "The stored secret is untouched."}
	}

	// 2xx and nothing else, which is `[[ "$http" != 2* ]]` at
	// lib/credentials.sh:304. 300 is the boundary that matters: a redirect the
	// client above did not follow arrives here as the vendor's answer, and
	// treating it as success would read an empty body as a token response.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &Refusal{
			Kind:   ErrRefresh,
			Reason: fmt.Sprintf("the vendor rejected the refresh (HTTP %d): %s", resp.StatusCode, rejectionReason(raw)),
			Action: "The stored secret is untouched, so the chain still holds until it expires. Re-seed it by hand if this keeps failing.",
		}
	}

	// A declared divergence, and this side is the better one. The shell hands
	// a 2xx body straight to `jq -r '.access_token // empty'`
	// (lib/credentials.sh:310): a body that is not a JSON object makes jq
	// error, `new_access` comes back empty, and the operator is told "the
	// vendor's response carried no access token" — which describes a vendor
	// that answered correctly and left one field out. What happened is that
	// the vendor did not answer with a token response at all, and an HTML
	// error page from a proxy is the common way it happens. The two need
	// different reactions, so they get different sentences.
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
// The shell is _cred_refusal_reason (lib/credentials.sh:265-268), and the three
// arms below are its three arms. It reads
//
//	(.error.message? // .error_description // (.error | strings) // "no reason given")
//
// with `|| printf 'no reason given'` behind it. The `?` and the `strings` guard
// are both recent: without the first, `.error.message` against a string is a
// hard jq error rather than a null, so jq exited 5 having printed nothing and
// `{"error":"invalid_client"}` — the shape RFC 6749 section 5.2 specifies, and
// the common one — lost its reason entirely; without the second, an object or
// an array `error` printed a raw multi-line JSON blob into a one-line status
// message. This function matches the fixed filter, arm for arm, and
// tests/test-credentials.sh asserts the same seven shapes against it.
//
// # One divergence, and it is this side that is right
//
// A value that is not a string reads as absent here and the shell prints it.
// `{"error_description":123}` makes `jq -r` print `123`; stringAt answers "",
// the chain falls through, and the message says "no reason given". A bare
// number is not a reason, and the shell's own last arm already agrees — the
// `strings` guard exists precisely so that a non-string prints nothing rather
// than a JSON fragment inside a status message. This applies the same
// judgement to the two arms above it as well as the one below.
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
// (lib/credentials.sh:320-325), and jq keeps a document's own key order,
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
func (o *object) setString(path []string, value string) {
	o.setRaw(path, encodeString(value))
}

func (o *object) setRaw(path []string, value json.RawMessage) {
	key := path[0]
	if _, present := o.values[key]; !present {
		o.keys = append(o.keys, key)
	}
	if len(path) == 1 {
		o.values[key] = value
		return
	}

	// An existing value that is not an object is replaced by one, which is what
	// jq does. There was a refusal here instead, and it was unreachable: the
	// only nested path this package writes is `.tokens`, and Refresh has
	// already refused a credential whose `.tokens.refresh_token` did not read
	// as a string — which a `.tokens` that is not an object cannot produce.
	child, err := decodeObject(o.values[key])
	if err != nil {
		child = &object{values: map[string]json.RawMessage{}}
	}
	child.setRaw(path[1:], value)
	o.values[key] = child.marshal()
}

// marshal renders the object compactly, in key order.
//
// It cannot fail either. Every value it writes is one of two things: bytes the
// decoder in UnmarshalJSON validated, or bytes this file produced. json.Compact
// refuses neither, so a value that arrived with whitespace inside it is
// compacted the way `jq -c` does and one that did not is copied.
func (o *object) marshal() []byte {
	var out bytes.Buffer
	out.WriteByte('{')
	for at, key := range o.keys {
		if at > 0 {
			out.WriteByte(',')
		}
		out.Write(encodeString(key))
		out.WriteByte(':')
		// Compact rather than re-encode: an untouched value keeps its own
		// literal, so a large integer stays an integer instead of being read
		// into a float64 and written back in exponent form.
		_ = json.Compact(&out, o.values[key])
	}
	out.WriteByte('}')
	return out.Bytes()
}

// encodeString renders one JSON string the way jq does.
//
// SetEscapeHTML(false) is the whole point: encoding/json turns `<`, `>` and `&`
// into <, > and & by default, and jq leaves them alone. Measured:
// `jq -c .` on `{"x":"a<b>&c"}` prints it back unchanged.
// It cannot fail, and says so rather than handing every caller an error to
// carry: the encoder writes into a bytes.Buffer, whose Write never returns one,
// and every Go string encodes — bytes that are not UTF-8 become U+FFFD rather
// than an error. The chain above it was written around that impossible failure
// and handled it six times.
func encodeString(value string) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	// Encode appends a newline.
	return bytes.TrimRight(out.Bytes(), "\n")
}
