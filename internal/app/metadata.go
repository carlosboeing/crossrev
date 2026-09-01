package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// RoleDefaultName is the App name CrossRev proposes for an owner
// (_auth_role_default_name, lib/auth.sh:97).
//
// The name is display text and takes the product name (ADR 0010): it is what a
// person reads in an organisation's installed Apps list, beside `Claude` and
// `Vercel`.
//
// The spaces are safe, and that is load-bearing rather than incidental. GitHub
// derives the slug by lowercasing and turning spaces into hyphens, so this
// spelling yields the slug the lowercase form did — and the slug is what the
// trusted-author check matches literally.
//
// The owner half keeps its own casing. That is an identity GitHub chose, not
// prose CrossRev gets to restyle.
//
// An unknown role matches no case arm and prints nothing, which is what the
// shell does rather than failing.
func RoleDefaultName(role, owner string) string {
	switch role {
	case RoleLoop:
		return "CrossRev " + owner
	case RoleRefresher:
		return "CrossRev Refresh " + owner
	}
	return ""
}

// RoleSummary is what a role is allowed to do, as `auth status` prints it
// (_auth_role_summary, lib/auth.sh:64).
func RoleSummary(role string) string {
	switch role {
	case RoleLoop:
		return "contents:write, issues:write, pull_requests:write"
	case RoleRefresher:
		return "secrets:write (repository secrets only)"
	}
	return ""
}

// RoleKeySecret is which repository secret carries a role's private key
// (_auth_role_key_secret, lib/auth.sh:77).
//
// Named per role rather than assumed, because the two are not interchangeable
// and the consequence of confusing them is not a broken deploy — it is the
// refresher's key material sitting behind the loop App's identity, which is the
// exact privilege separation the two Apps exist to draw.
func RoleKeySecret(role string) string {
	switch role {
	case RoleLoop:
		return "APP_PRIVATE_KEY"
	case RoleRefresher:
		return "CROSSREV_REFRESH_APP_PRIVATE_KEY"
	}
	return ""
}

// Slug derives an App's slug from its name the way GitHub does: lowercase,
// spaces to hyphens (_auth_slug, lib/auth.sh:105).
//
// The lowercasing is ASCII-only, and that is a decision rather than a
// shortcut. `tr '[:upper:]' '[:lower:]'` answers differently in different
// locales — measured, it lowercases U+00DC under en_AU.UTF-8 and leaves it
// alone under LC_ALL=C, which is what a runner has. The C answer is the one
// that does not move between a laptop and a runner, and GitHub's App names are
// ASCII anyway. strings.ToLower would take the other side and diverge on a
// runner.
//
// Only U+0020 becomes a hyphen. `tr ' '` names one character, so a tab stays a
// tab.
func Slug(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		case c == ' ':
			b.WriteByte('-')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Metadata is the file `auth login` writes beside an App's private key
// (lib/auth.sh:708-714), in the order jq wrote its keys.
//
// It is a struct rather than a map so the field order is the file's order, and
// so a caller reading .Slug is reading the field the trusted-author check falls
// back to rather than a string keyed by a name it spelled itself.
type Metadata struct {
	Owner     string `json:"owner"`
	OwnerType string `json:"owner_type"`
	OwnerID   int64  `json:"owner_id"`
	ID        int64  `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Created   string `json:"created"`
}

// ReadMetadata reads one App's cached identity.
//
// An absent role is the loop's: anything registered before roles existed has no
// role key, and `auth status` reads it as `.role // "loop"` (lib/auth.sh:382).
func ReadMetadata(path string) (Metadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("could not read the App metadata: %w", err)
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return Metadata{}, fmt.Errorf("could not read the App metadata at %s: %w", path, err)
	}
	if meta.Role == "" {
		meta.Role = RoleLoop
	}
	return meta, nil
}

// Drift is one field of the cached identity that no longer matches the one
// GitHub has.
//
// The shell prints these as tab-separated text and the caller splits it, with
// tabs rather than spaces because an App name is free text and routinely
// contains spaces — CrossRev proposes one that does. Three fields make the
// split unnecessary rather than careful.
type Drift struct {
	Field string
	Was   string
	Now   string
}

// SyncMeta reconciles the cached identity at path against the authoritative
// one, correcting it (_auth_sync_meta, lib/auth.sh:207).
//
// It returns one entry per field that moved, and nothing at all when the two
// agree — in which case the file is not touched.
//
// It writes, which a status command otherwise does not. The justification is
// that this file is CrossRev's own cache of a fact GitHub owns, not operator
// config, and the cached slug is what the trusted-author check falls back to.
// Diagnosing that drift and then leaving it in place would report a fault it
// had already found and could have fixed, and the only repair left would be
// editing JSON by hand.
//
// The rewrite goes to a sibling and is renamed over the original, created 0600
// rather than created and then chmodded, so nothing ever reads a half-written
// file or one briefly wider than the 0600 the original was created with.
func SyncMeta(path, name, slug string) ([]Drift, error) {
	// The shell reads the two fields with jq and discards its diagnostics, so
	// an unreadable or unparseable file yields two empty strings rather than a
	// refusal here. What refuses is the write below, which cannot rewrite what
	// it could not read.
	obj, readErr := readObject(path)
	was := func(field string) string {
		if readErr != nil {
			return ""
		}
		return obj.stringValue(field)
	}

	var drift []Drift
	if wasName := was("name"); wasName != name {
		drift = append(drift, Drift{Field: "name", Was: wasName, Now: name})
	}
	if wasSlug := was("slug"); wasSlug != slug {
		drift = append(drift, Drift{Field: "slug", Was: wasSlug, Now: slug})
	}
	if len(drift) == 0 {
		return nil, nil
	}
	if readErr != nil {
		return nil, fmt.Errorf("could not correct the cached App identity at %s: %w", path, readErr)
	}

	obj.set("name", mustEncodeString(name))
	obj.set("slug", mustEncodeString(slug))
	if err := writeFile0600(path, obj.indented()); err != nil {
		return nil, fmt.Errorf("could not correct the cached App identity at %s: %w", path, err)
	}
	return drift, nil
}

// writeFile0600 replaces path with data, through a sibling named the way the
// shell names it.
//
// The temporary file is removed when the write fails. The shell leaves it,
// which is a wart rather than a contract: nothing reads it and the next attempt
// truncates it.
func writeFile0600(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// --- an object that keeps its key order ------------------------------------
//
// jq rewrites the two fields it was given and copies every other key through in
// the order it found them. A Go map does neither, and a struct drops a key this
// version has not heard of — an operator's, or a later version's. This is the
// smallest thing that behaves the way the file's existing readers already do.

type object struct {
	keys   []string
	values map[string]json.RawMessage
}

// readObject parses a JSON object from a file, keeping key order.
func readObject(path string) (*object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeObject(data)
}

func decodeObject(data []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object")
	}
	obj := &object{values: make(map[string]json.RawMessage)}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected a JSON object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		obj.set(key, raw)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return obj, nil
}

// set replaces a key's value in place, or appends it when the key is new. That
// is `.k = v` at the shell: measured, an existing key keeps its position and a
// new one lands at the end.
func (o *object) set(key string, raw json.RawMessage) {
	if _, held := o.values[key]; !held {
		o.keys = append(o.keys, key)
	}
	o.values[key] = raw
}

func (o *object) get(key string) (json.RawMessage, bool) {
	raw, held := o.values[key]
	return raw, held
}

// stringValue is `jq -r '.k // empty'` for a string field: the value when it is
// a string, and empty for anything else — absent, null, or another type.
func (o *object) stringValue(key string) string {
	raw, held := o.get(key)
	if !held {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// compact is the object as `jq -c` writes it.
func (o *object) compact() []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(mustEncodeString(key))
		buf.WriteByte(':')
		// The value is re-compacted rather than copied, so a file somebody
		// pretty-printed by hand comes back out the way jq would write it.
		var value bytes.Buffer
		if err := json.Compact(&value, o.values[key]); err != nil {
			value.Write(o.values[key])
		}
		buf.Write(value.Bytes())
	}
	buf.WriteByte('}')
	buf.WriteByte('\n')
	return buf.Bytes()
}

// indented is the object as jq writes it without -c: two spaces of indent, a
// space after each colon, and a trailing newline. Verified byte for byte
// against jq, nested values and empty containers included.
func (o *object) indented() []byte {
	compact := o.compact()
	var buf bytes.Buffer
	if err := json.Indent(&buf, bytes.TrimRight(compact, "\n"), "", "  "); err != nil {
		return compact
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// mustEncodeString is one JSON string, escaped the way jq escapes it.
//
// SetEscapeHTML(false) is the whole point: encoding/json escapes <, > and & by
// default and jq does not, so an App name carrying one would come back changed.
func mustEncodeString(s string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		// encoding a string cannot fail; a bare quoted empty string is the
		// fail-closed answer if it ever does.
		return json.RawMessage(`""`)
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n"))
}
