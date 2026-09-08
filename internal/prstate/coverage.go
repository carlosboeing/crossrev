package prstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// coverage.go — the append-only coverage codec behind the `<!-- crossrev:c `
// marker (plan Task B1).
//
// The ledger holds one complete generation per manifest: a manifest comment
// naming every shard comment by id, position, digest and record count, and one
// comment per shard. No coverage comment is ever edited; a new generation
// writes new comments and the manifest last. Readers refuse missing, altered
// or reordered bytes rather than reading them as empty coverage.
//
// Every integrity digest is full-strength SHA-256 over 64 lowercase hex
// characters, and each digest excludes its own field: the writer digests the
// bytes without the field, writes the value into it, and posts. A reader
// removes the field again before recomputing. Unit identities stay 16 hex
// under the design's u1 file preimage; body digests stay full 64-hex SHA-256
// over the evidence bytes.
//
// Verification is not implemented in this release. The manifest carries a
// `verification` object with `status: not_implemented` and its five other
// declared fields null. The envelope types reserve the future values — the
// status enum, the nullable revision, the opaque arrays, the nullable
// recovery — so decoding and re-encoding preserve another writer's populated
// values without interpreting them. This release's own new generations write
// `not_implemented` and five nulls, never synthesize evidence, and never copy
// another engine's verification record into a new generation.

const coverageSchemaVersion = 1

// CoverageSchemaVersion is the `v` every coverage manifest and shard opens
// with. A reader refuses any other version rather than guessing at it.
func CoverageSchemaVersion() int { return coverageSchemaVersion }

// Coverage kinds. No progress, slot, rollup, clarification, dependency or
// compressed kind exists: strict typed decoding refuses any other value.
const (
	CoverageKindManifest = "manifest"
	CoverageKindShard    = "shard"
)

// Coverage record types. Outstanding is a record type, not a disposition:
// there is no pending judgement.
const (
	CoverageRecordUnit        = "unit"
	CoverageRecordOutstanding = "outstanding"
)

// Coverage granularities. This release runs at file granularity only.
const CoverageGranularityFile = "file"

// Verification statuses. This release's writers emit only NotImplemented;
// the rest reserve the envelope for verification work without interpreting
// the contents here.
const (
	VerificationNotImplemented     = "not_implemented"
	VerificationPassed             = "passed"
	VerificationFailed             = "failed"
	VerificationPending            = "pending"
	VerificationUnavailable        = "unavailable"
	VerificationNoChecksDiscovered = "no_checks_discovered"
)

// VerificationStatuses lists every declared envelope status, in the order the
// reservation names them.
func VerificationStatuses() []string {
	return []string{
		VerificationNotImplemented,
		VerificationPassed,
		VerificationFailed,
		VerificationPending,
		VerificationUnavailable,
		VerificationNoChecksDiscovered,
	}
}

// ErrCoverage is returned for coverage bytes no strict reader accepts.
var ErrCoverage = errors.New("a coverage payload is not a v1 manifest or shard")

// IsCoverageError reports whether err is the coverage codec's refusal.
func IsCoverageError(err error) bool { return errors.Is(err, ErrCoverage) }

func coverageErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCoverage, fmt.Sprintf(format, args...))
}

// Verification is the reserved verification envelope. Status declares the
// nullable enum; Revision declares string|null; Checks, SourcesInspected and
// Rejected declare array|null as opaque JSON arrays with no element type in
// this release; Recovery declares string|null.
//
// The reserved arrays are opaque on purpose: no CheckResult type, no element
// validator and no interpretation ship here. Decoding and re-encoding preserve
// populated reserved values byte for byte; this release never reads them as
// evidence.
type Verification struct {
	// Status is the envelope status, or empty when absent. Present values are
	// validated against the declared enum on decode.
	Status Opt[string] `json:"status,omitzero"`
	// Revision is the verified revision, or null when absent.
	Revision Opt[string] `json:"revision,omitzero"`
	// Checks, SourcesInspected and Rejected are opaque reserved arrays, or
	// null when absent. Each is kept as raw bytes so populated values
	// round-trip without this release naming an element type.
	Checks           Opt[json.RawMessage] `json:"checks,omitzero"`
	SourcesInspected Opt[json.RawMessage] `json:"sources_inspected,omitzero"`
	Rejected         Opt[json.RawMessage] `json:"rejected,omitzero"`
	// Recovery is the recovery text, or null when absent.
	Recovery Opt[string] `json:"recovery,omitzero"`
}

// UnimplementedVerification is the only verification envelope this release
// writes: `not_implemented` with revision, checks, sources_inspected,
// rejected and recovery all null. Writers call this rather than building the
// envelope by hand so a new generation cannot claim a result, carry an empty
// result array, or copy another engine's evidence into its own bytes.
func UnimplementedVerification() Verification {
	return Verification{
		Status:           Some(VerificationNotImplemented),
		Revision:         Null[string](),
		Checks:           Null[json.RawMessage](),
		SourcesInspected: Null[json.RawMessage](),
		Rejected:         Null[json.RawMessage](),
		Recovery:         Null[string](),
	}
}

// Evidence is what one coverage record's judgement rests on: a supplied path
// and revision, nullable line spans, an evidence source, and a nullable note.
// Line endpoints are null for file-level evidence.
type Evidence struct {
	Path      string      `json:"path"`
	Revision  Opt[string] `json:"revision,omitzero"`
	StartLine Opt[int]    `json:"start_line,omitzero"`
	EndLine   Opt[int]    `json:"end_line,omitzero"`
	Source    string      `json:"source"`
	Note      Opt[string] `json:"note,omitzero"`
}

// Record is one required file inside a shard: either a judged unit carrying
// its disposition, finding ids, evidence and reason, or an outstanding record
// carrying no disposition or finding ids. The kind is fixed to file; the
// change is one of the five enumerations.
type Record struct {
	Type        string      `json:"type"`
	UnitID      string      `json:"unit_id"`
	PathIndex   int         `json:"path_index"`
	Kind        string      `json:"kind"`
	Change      string      `json:"change"`
	BodyDigest  string      `json:"body_digest"`
	Disposition Opt[string] `json:"disposition,omitzero"`
	FindingIDs  []string    `json:"finding_ids,omitzero"`
	Evidence    []Evidence  `json:"evidence,omitzero"`
	Reason      Opt[string] `json:"reason,omitzero"`
}

// AdvisoryLimit is one visible cap: a search term whose holders exceeded the
// per-term budget. Rule names the capped term as "search:<term>".
type AdvisoryLimit struct {
	Rule     string `json:"rule"`
	Observed int    `json:"observed"`
	Limit    int    `json:"limit"`
	Reason   string `json:"reason"`
}

// Advisory is the persisted advisory answer for one generation: the untouched
// context count, the rules that ran, and the caps hit along the way.
// Advisory members are reconstructed, not durably enumerated, so no member
// rows ship here.
type Advisory struct {
	Count  int             `json:"count"`
	Rules  []string        `json:"rules"`
	Limits []AdvisoryLimit `json:"limits"`
}

// CoverageExclusion is one path visibly removed from the required
// denominator, with the reason it was removed.
type CoverageExclusion struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ScopeReport is the stored scope report: the accepted model response's
// examined scope and known limits, labelled as reviewer claims rather than
// deterministic discovery.
type ScopeReport struct {
	ExaminedScope string   `json:"examined_scope"`
	KnownLimits   []string `json:"known_limits"`
}

// ShardRef binds one shard's position, comment id, full SHA-256 integrity
// digest and record count.
type ShardRef struct {
	Pos    int    `json:"pos"`
	ID     int64  `json:"id"`
	Digest string `json:"digest"`
	N      int    `json:"n"`
}

// Manifest is one complete coverage generation: the revision pair and engine
// it was made at, the path table, the shard references, the required and
// outstanding counts, the advisory and excluded summaries, the reviewer scope
// report, the reserved verification envelope, and the full SHA-256 integrity
// digest over its own bytes with the digest field removed.
type Manifest struct {
	Version          int                 `json:"v"`
	Kind             string              `json:"kind"`
	Gen              int                 `json:"gen"`
	BaseSHA          string              `json:"base_sha"`
	HeadSHA          string              `json:"head_sha"`
	Engine           string              `json:"engine"`
	Granularity      string              `json:"granularity"`
	Paths            []string            `json:"paths"`
	Shards           []ShardRef          `json:"shards"`
	OutstandingCount int                 `json:"outstanding_count"`
	RequiredCount    int                 `json:"required_count"`
	Advisory         Advisory            `json:"advisory"`
	Excluded         []CoverageExclusion `json:"excluded"`
	ScopeReport      ScopeReport         `json:"scope_report"`
	Verification     Verification        `json:"verification"`
	Digest           string              `json:"digest"`

	// commentID is which comment the manifest was read off, and raw is the
	// bytes it was read as. Both are unexported so no encoder can reach
	// them; the accessors below are the only route.
	commentID int64
	raw       json.RawMessage
}

// CommentID is which comment the manifest was read off, or zero for a
// manifest that was built rather than read.
func (m Manifest) CommentID() int64 { return m.commentID }

// Raw is the manifest exactly as DecodeCoverageManifest read it. The bytes
// are a copy, so editing them cannot reach the manifest.
func (m Manifest) Raw() json.RawMessage { return bytes.Clone(m.raw) }

// Shard is one shard comment: its position, its records, and the full
// SHA-256 integrity digest over its own bytes with the digest field removed.
// A shard carries no generation number: membership is established by the
// manifest that names it.
type Shard struct {
	Version int      `json:"v"`
	Kind    string   `json:"kind"`
	Pos     int      `json:"pos"`
	Records []Record `json:"records"`
	Digest  string   `json:"digest"`

	commentID int64
	raw       json.RawMessage
}

// CommentID is which comment the shard was read off, or zero for a shard
// that was built rather than read.
func (s Shard) CommentID() int64 { return s.commentID }

// Raw is the shard exactly as DecodeCoverageShard read it. The bytes are a
// copy, so editing them cannot reach the shard.
func (s Shard) Raw() json.RawMessage { return bytes.Clone(s.raw) }

// CoverageStop is the incomplete stop diagnostics a halted pass records.
// The pass's head SHA and halt reason bind these diagnostics to the failed
// publication; they never count as a complete generation.
type CoverageStop struct {
	RequiredCount    int    `json:"required_count"`
	CoveredCount     int    `json:"covered_count"`
	OutstandingCount int    `json:"outstanding_count"`
	MeasuredBytes    int    `json:"measured_bytes"`
	ShardCount       int    `json:"shard_count"`
	Limit            string `json:"limit"`
}

// EncodeCoverageManifest serialises a manifest for embedding in a comment
// body. The manifest digest is recomputed from bytes with the digest member
// removed, so a caller-supplied digest can neither survive nor mismatch: the
// bytes on the wire always carry the digest of the bytes on the wire.
func EncodeCoverageManifest(m Manifest) (string, error) {
	m.Digest = ""
	raw := json.RawMessage(manifestFields(m).marshal())
	digest, err := digestWithoutField(raw, "digest")
	if err != nil {
		return "", coverageErrorf("encoding a coverage manifest: %v", err)
	}
	m.Digest = digest
	raw = json.RawMessage(manifestFields(m).marshal())
	normalised, err := normalise(raw)
	if err != nil {
		return "", coverageErrorf("encoding a coverage manifest: %v", err)
	}
	return "\n\n" + coverageMarkerOpen + string(normalised) + markerClose, nil
}

// EncodeCoverageShard serialises a shard for embedding in a comment body.
// The shard digest is recomputed from bytes with the digest member removed,
// for the same reason the manifest's is.
func EncodeCoverageShard(s Shard) (string, error) {
	s.Digest = ""
	raw := json.RawMessage(shardFields(s).marshal())
	digest, err := digestWithoutField(raw, "digest")
	if err != nil {
		return "", coverageErrorf("encoding a coverage shard: %v", err)
	}
	s.Digest = digest
	raw = json.RawMessage(shardFields(s).marshal())
	normalised, err := normalise(raw)
	if err != nil {
		return "", coverageErrorf("encoding a coverage shard: %v", err)
	}
	return "\n\n" + coverageMarkerOpen + string(normalised) + markerClose, nil
}

// DecodeCoverageManifest pulls a manifest out of one comment body, checks its
// shape strictly, and verifies its integrity digest before returning it. A
// body carrying no coverage marker, a payload of any other kind, or a digest
// mismatch decodes to nothing.
func DecodeCoverageManifest(body string) (Manifest, bool) {
	payload := extractCoveragePayload(body)
	if payload == "" {
		return Manifest{}, false
	}
	m, ok := decodeManifestPayload([]byte(payload))
	if !ok {
		return Manifest{}, false
	}
	m.commentID = 0
	m.raw = json.RawMessage(payload)
	return m, true
}

// DecodeCoverageShard pulls a shard out of one comment body, checks its shape
// strictly, and verifies its integrity digest before returning it.
func DecodeCoverageShard(body string) (Shard, bool) {
	payload := extractCoveragePayload(body)
	if payload == "" {
		return Shard{}, false
	}
	s, ok := decodeShardPayload([]byte(payload))
	if !ok {
		return Shard{}, false
	}
	s.commentID = 0
	s.raw = json.RawMessage(payload)
	return s, true
}

// coverageMarkerOpen is the coverage opening delimiter: the prefix plus its
// trailing space, 16 characters in all.
const coverageMarkerOpen = CoverageMarkerPrefix + " "

// extractCoveragePayload applies the per-line rule the marker decoder
// applies: on each line, the text after the LAST coverage opening delimiter
// and before the LAST closing one after it, with every line's result
// concatenated.
func extractCoveragePayload(body string) string {
	var out strings.Builder
	for line := range strings.SplitSeq(body, "\n") {
		i := strings.LastIndex(line, coverageMarkerOpen)
		if i < 0 {
			continue
		}
		rest := line[i+len(coverageMarkerOpen):]
		j := strings.LastIndex(rest, markerClose)
		if j < 0 {
			continue
		}
		out.WriteString(rest[:j])
	}
	return out.String()
}

// digestWithoutField digests the object's bytes with one member removed. The
// writer digests the body without the field, writes the value into it, and
// posts; a reader removes the field again before recomputing. The rule is one
// rule at both levels rather than two.
func digestWithoutField(raw json.RawMessage, field string) (string, error) {
	stripped, err := stripKey(raw, field)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(stripped)
	return hex.EncodeToString(sum[:]), nil
}

// verifyDigest recomputes the digest over the payload with the digest member
// removed and compares it against the carried value.
func verifyDigest(raw json.RawMessage, carried string) bool {
	recomputed, err := digestWithoutField(raw, "digest")
	if err != nil {
		return false
	}
	return recomputed == carried
}

// manifestFields orders the manifest keys as the ledger writes them. Go sorts
// a map's keys and `jq -c` keeps insertion order, so the encoded bytes keep
// this order the way the pass marker keeps its writers' order.
func manifestFields(m Manifest) object {
	return object{
		{key: "v", value: json.RawMessage(fmt.Sprintf("%d", m.Version))},
		{key: "kind", value: appendJSONString(nil, m.Kind)},
		{key: "gen", value: json.RawMessage(fmt.Sprintf("%d", m.Gen))},
		{key: "base_sha", value: appendJSONString(nil, m.BaseSHA)},
		{key: "head_sha", value: appendJSONString(nil, m.HeadSHA)},
		{key: "engine", value: appendJSONString(nil, m.Engine)},
		{key: "granularity", value: appendJSONString(nil, m.Granularity)},
		{key: "paths", value: stringsOf(m.Paths)},
		{key: "shards", value: shardRefsOf(m.Shards)},
		{key: "outstanding_count", value: json.RawMessage(fmt.Sprintf("%d", m.OutstandingCount))},
		{key: "required_count", value: json.RawMessage(fmt.Sprintf("%d", m.RequiredCount))},
		{key: "advisory", value: advisoryOf(m.Advisory)},
		{key: "excluded", value: exclusionsOf(m.Excluded)},
		{key: "scope_report", value: scopeReportOf(m.ScopeReport)},
		{key: "verification", value: verificationOf(m.Verification)},
		{key: "digest", value: appendJSONString(nil, m.Digest)},
	}
}

// shardFields orders the shard keys as the ledger writes them.
func shardFields(s Shard) object {
	return object{
		{key: "v", value: json.RawMessage(fmt.Sprintf("%d", s.Version))},
		{key: "kind", value: appendJSONString(nil, s.Kind)},
		{key: "pos", value: json.RawMessage(fmt.Sprintf("%d", s.Pos))},
		{key: "records", value: recordsOf(s.Records)},
		{key: "digest", value: appendJSONString(nil, s.Digest)},
	}
}

func stringsOf(in []string) json.RawMessage {
	out := []byte{'['}
	for i, s := range in {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendJSONString(out, s)
	}
	return append(out, ']')
}

func optStringOf(o Opt[string]) json.RawMessage {
	if !o.Present() {
		return json.RawMessage("null")
	}
	if o.IsNull() {
		return json.RawMessage("null")
	}
	return appendJSONString(nil, o.Value())
}

func optIntOf(o Opt[int]) json.RawMessage {
	if !o.Present() || o.IsNull() {
		return json.RawMessage("null")
	}
	return json.RawMessage(fmt.Sprintf("%d", o.Value()))
}

func optRawOf(o Opt[json.RawMessage]) json.RawMessage {
	if !o.Present() || o.IsNull() {
		return json.RawMessage("null")
	}
	raw := o.Value()
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null")
	}
	compact, err := compactValue(raw)
	if err != nil {
		return json.RawMessage("null")
	}
	return compact
}

func evidenceOf(e Evidence) json.RawMessage {
	return object{
		{key: "path", value: appendJSONString(nil, e.Path)},
		{key: "revision", value: optStringOf(e.Revision)},
		{key: "start_line", value: optIntOf(e.StartLine)},
		{key: "end_line", value: optIntOf(e.EndLine)},
		{key: "source", value: appendJSONString(nil, e.Source)},
		{key: "note", value: optStringOf(e.Note)},
	}.marshal()
}

func evidencesOf(in []Evidence) json.RawMessage {
	out := []byte{'['}
	for i, e := range in {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, evidenceOf(e)...)
	}
	return append(out, ']')
}

func recordOf(r Record) json.RawMessage {
	obj := object{
		{key: "type", value: appendJSONString(nil, r.Type)},
		{key: "unit_id", value: appendJSONString(nil, r.UnitID)},
		{key: "path_index", value: json.RawMessage(fmt.Sprintf("%d", r.PathIndex))},
		{key: "kind", value: appendJSONString(nil, r.Kind)},
		{key: "change", value: appendJSONString(nil, r.Change)},
		{key: "body_digest", value: appendJSONString(nil, r.BodyDigest)},
		{key: "disposition", value: optStringOf(r.Disposition)},
	}
	if r.FindingIDs == nil {
		obj = append(obj, member{key: "finding_ids", value: json.RawMessage("null")})
	} else {
		obj = append(obj, member{key: "finding_ids", value: stringsOf(r.FindingIDs)})
	}
	if r.Evidence == nil {
		obj = append(obj, member{key: "evidence", value: json.RawMessage("[]")})
	} else {
		obj = append(obj, member{key: "evidence", value: evidencesOf(r.Evidence)})
	}
	obj = append(obj, member{key: "reason", value: optStringOf(r.Reason)})
	return obj.marshal()
}

func recordsOf(in []Record) json.RawMessage {
	out := []byte{'['}
	for i, r := range in {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, recordOf(r)...)
	}
	return append(out, ']')
}

func advisoryOf(a Advisory) json.RawMessage {
	limits := []byte{'['}
	for i, l := range a.Limits {
		if i > 0 {
			limits = append(limits, ',')
		}
		limits = append(limits, object{
			{key: "rule", value: appendJSONString(nil, l.Rule)},
			{key: "observed", value: json.RawMessage(fmt.Sprintf("%d", l.Observed))},
			{key: "limit", value: json.RawMessage(fmt.Sprintf("%d", l.Limit))},
			{key: "reason", value: appendJSONString(nil, l.Reason)},
		}.marshal()...)
	}
	limits = append(limits, ']')
	return object{
		{key: "count", value: json.RawMessage(fmt.Sprintf("%d", a.Count))},
		{key: "rules", value: stringsOf(a.Rules)},
		{key: "limits", value: json.RawMessage(limits)},
	}.marshal()
}

func exclusionsOf(in []CoverageExclusion) json.RawMessage {
	out := []byte{'['}
	for i, e := range in {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, object{
			{key: "path", value: appendJSONString(nil, e.Path)},
			{key: "reason", value: appendJSONString(nil, e.Reason)},
		}.marshal()...)
	}
	return append(out, ']')
}

func scopeReportOf(s ScopeReport) json.RawMessage {
	limits := s.KnownLimits
	if limits == nil {
		limits = []string{}
	}
	return object{
		{key: "examined_scope", value: appendJSONString(nil, s.ExaminedScope)},
		{key: "known_limits", value: stringsOf(limits)},
	}.marshal()
}

func verificationOf(v Verification) json.RawMessage {
	return object{
		{key: "status", value: optStringOf(v.Status)},
		{key: "revision", value: optStringOf(v.Revision)},
		{key: "checks", value: optRawOf(v.Checks)},
		{key: "sources_inspected", value: optRawOf(v.SourcesInspected)},
		{key: "rejected", value: optRawOf(v.Rejected)},
		{key: "recovery", value: optStringOf(v.Recovery)},
	}.marshal()
}

func shardRefOf(r ShardRef) json.RawMessage {
	return object{
		{key: "pos", value: json.RawMessage(fmt.Sprintf("%d", r.Pos))},
		{key: "id", value: json.RawMessage(fmt.Sprintf("%d", r.ID))},
		{key: "digest", value: appendJSONString(nil, r.Digest)},
		{key: "n", value: json.RawMessage(fmt.Sprintf("%d", r.N))},
	}.marshal()
}

func shardRefsOf(in []ShardRef) json.RawMessage {
	out := []byte{'['}
	for i, r := range in {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, shardRefOf(r)...)
	}
	return append(out, ']')
}

// decodeManifestPayload strictly decodes one manifest payload: every declared
// member has its declared type, no member is unknown, and the digest matches
// the bytes with the digest member removed. Unknown or mistyped members are
// refused, because a progress, slot, rollup, clarification, dependency or
// compressed payload must never enter the ledger as coverage.
func decodeManifestPayload(raw []byte) (Manifest, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Manifest{}, false
	}
	want := []string{"v", "kind", "gen", "base_sha", "head_sha", "engine", "granularity",
		"paths", "shards", "outstanding_count", "required_count", "advisory",
		"excluded", "scope_report", "verification", "digest"}
	if !exactKeys(obj, want) {
		return Manifest{}, false
	}
	get := func(key string) json.RawMessage {
		v, _ := obj.get(key)
		return v
	}
	version, ok := decodeInt(get("v"))
	if !ok || version != coverageSchemaVersion {
		return Manifest{}, false
	}
	var kind string
	if err := json.Unmarshal(get("kind"), &kind); err != nil || kind != CoverageKindManifest {
		return Manifest{}, false
	}
	gen, ok := decodeInt(get("gen"))
	if !ok {
		return Manifest{}, false
	}
	var baseSHA, headSHA, engine, granularity, digest string
	if err := json.Unmarshal(get("base_sha"), &baseSHA); err != nil || baseSHA == "" {
		return Manifest{}, false
	}
	if err := json.Unmarshal(get("head_sha"), &headSHA); err != nil || headSHA == "" {
		return Manifest{}, false
	}
	if err := json.Unmarshal(get("engine"), &engine); err != nil || engine == "" {
		return Manifest{}, false
	}
	if err := json.Unmarshal(get("granularity"), &granularity); err != nil || granularity != CoverageGranularityFile {
		return Manifest{}, false
	}
	if err := json.Unmarshal(get("digest"), &digest); err != nil || !isHex64(digest) {
		return Manifest{}, false
	}
	paths, ok := decodeStrings(get("paths"))
	if !ok {
		return Manifest{}, false
	}
	shards, ok := decodeShardRefs(get("shards"))
	if !ok {
		return Manifest{}, false
	}
	outstanding, ok := decodeInt(get("outstanding_count"))
	if !ok {
		return Manifest{}, false
	}
	required, ok := decodeInt(get("required_count"))
	if !ok {
		return Manifest{}, false
	}
	advisory, ok := decodeAdvisory(get("advisory"))
	if !ok {
		return Manifest{}, false
	}
	excluded, ok := decodeExclusions(get("excluded"))
	if !ok {
		return Manifest{}, false
	}
	report, ok := decodeScopeReport(get("scope_report"))
	if !ok {
		return Manifest{}, false
	}
	verification, ok := decodeVerification(get("verification"))
	if !ok {
		return Manifest{}, false
	}
	m := Manifest{
		Version:          version,
		Kind:             kind,
		Gen:              gen,
		BaseSHA:          baseSHA,
		HeadSHA:          headSHA,
		Engine:           engine,
		Granularity:      granularity,
		Paths:            paths,
		Shards:           shards,
		OutstandingCount: outstanding,
		RequiredCount:    required,
		Advisory:         advisory,
		Excluded:         excluded,
		ScopeReport:      report,
		Verification:     verification,
		Digest:           digest,
	}
	if !verifyDigest(raw, digest) {
		return Manifest{}, false
	}
	return m, true
}

// decodeShardPayload strictly decodes one shard payload: every declared
// member has its declared type, no member is unknown, and the digest matches
// the bytes with the digest member removed.
func decodeShardPayload(raw []byte) (Shard, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Shard{}, false
	}
	if !exactKeys(obj, []string{"v", "kind", "pos", "records", "digest"}) {
		return Shard{}, false
	}
	get := func(key string) json.RawMessage {
		v, _ := obj.get(key)
		return v
	}
	version, ok := decodeInt(get("v"))
	if !ok || version != coverageSchemaVersion {
		return Shard{}, false
	}
	var kind string
	if err := json.Unmarshal(get("kind"), &kind); err != nil || kind != CoverageKindShard {
		return Shard{}, false
	}
	pos, ok := decodeInt(get("pos"))
	if !ok {
		return Shard{}, false
	}
	var digest string
	if err := json.Unmarshal(get("digest"), &digest); err != nil || !isHex64(digest) {
		return Shard{}, false
	}
	records, ok := decodeRecords(get("records"))
	if !ok {
		return Shard{}, false
	}
	s := Shard{Version: version, Kind: kind, Pos: pos, Records: records, Digest: digest}
	if !verifyDigest(raw, digest) {
		return Shard{}, false
	}
	return s, true
}

// exactKeys reports whether obj holds exactly the named keys, in any order.
// An unknown member refuses the payload: a generation carrying a field this
// release does not declare is a newer or foreign generation, not an older
// one this release may read as its own.
func exactKeys(obj object, want []string) bool {
	if len(obj) != len(want) {
		return false
	}
	for _, key := range want {
		if obj.index(key) < 0 {
			return false
		}
	}
	return true
}

// decodeInt reads a JSON number holding a whole value at or above zero. The
// ledger counts and positions are non-negative; a fractional or negative
// literal is a mistyped member.
func decodeInt(raw json.RawMessage) (int, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	s := n.String()
	var i int
	if err := json.Unmarshal([]byte(s), &i); err != nil {
		return 0, false
	}
	var back float64
	if err := json.Unmarshal([]byte(s), &back); err != nil {
		return 0, false
	}
	if float64(i) != back || i < 0 {
		return 0, false
	}
	return i, true
}

// decodeInt64 reads a JSON number holding a whole value, signed, for comment
// ids. Ids arrive from GitHub as integers and may be large.
func decodeInt64(raw json.RawMessage) (int64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	var i int64
	if err := json.Unmarshal([]byte(n.String()), &i); err != nil {
		return 0, false
	}
	var back float64
	if err := json.Unmarshal([]byte(n.String()), &back); err != nil {
		return 0, false
	}
	if float64(i) != back {
		return 0, false
	}
	return i, true
}

func decodeStrings(raw json.RawMessage) ([]string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || raw[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var out []string
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return nil, false
		}
		var s string
		if err := json.Unmarshal(element, &s); err != nil {
			return nil, false
		}
		out = append(out, s)
	}
	if out == nil {
		out = []string{}
	}
	return out, true
}

func decodeShardRefs(raw json.RawMessage) ([]ShardRef, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || raw[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var out []ShardRef
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return nil, false
		}
		obj, err := parseObject(element)
		if err != nil || !exactKeys(obj, []string{"pos", "id", "digest", "n"}) {
			return nil, false
		}
		pos, ok := decodeInt(mustGet(obj, "pos"))
		if !ok {
			return nil, false
		}
		id, ok := decodeInt64(mustGet(obj, "id"))
		if !ok {
			return nil, false
		}
		var digest string
		if err := json.Unmarshal(mustGet(obj, "digest"), &digest); err != nil || !isHex64(digest) {
			return nil, false
		}
		n, ok := decodeInt(mustGet(obj, "n"))
		if !ok {
			return nil, false
		}
		out = append(out, ShardRef{Pos: pos, ID: id, Digest: digest, N: n})
	}
	if out == nil {
		out = []ShardRef{}
	}
	return out, true
}

func mustGet(obj object, key string) json.RawMessage {
	v, _ := obj.get(key)
	return v
}

func decodeAdvisory(raw json.RawMessage) (Advisory, bool) {
	obj, err := parseObject(raw)
	if err != nil || !exactKeys(obj, []string{"count", "rules", "limits"}) {
		return Advisory{}, false
	}
	count, ok := decodeInt(mustGet(obj, "count"))
	if !ok {
		return Advisory{}, false
	}
	rules, ok := decodeStrings(mustGet(obj, "rules"))
	if !ok {
		return Advisory{}, false
	}
	limitsRaw := mustGet(obj, "limits")
	if len(bytes.TrimSpace(limitsRaw)) == 0 || limitsRaw[0] != '[' {
		return Advisory{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(limitsRaw))
	if _, err := dec.Token(); err != nil {
		return Advisory{}, false
	}
	var limits []AdvisoryLimit
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return Advisory{}, false
		}
		entry, err := parseObject(element)
		if err != nil || !exactKeys(entry, []string{"rule", "observed", "limit", "reason"}) {
			return Advisory{}, false
		}
		var rule, reason string
		if err := json.Unmarshal(mustGet(entry, "rule"), &rule); err != nil {
			return Advisory{}, false
		}
		observed, ok := decodeInt(mustGet(entry, "observed"))
		if !ok {
			return Advisory{}, false
		}
		limit, ok := decodeInt(mustGet(entry, "limit"))
		if !ok {
			return Advisory{}, false
		}
		if err := json.Unmarshal(mustGet(entry, "reason"), &reason); err != nil {
			return Advisory{}, false
		}
		limits = append(limits, AdvisoryLimit{Rule: rule, Observed: observed, Limit: limit, Reason: reason})
	}
	if limits == nil {
		limits = []AdvisoryLimit{}
	}
	return Advisory{Count: count, Rules: rules, Limits: limits}, true
}

func decodeExclusions(raw json.RawMessage) ([]CoverageExclusion, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || raw[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var out []CoverageExclusion
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return nil, false
		}
		obj, err := parseObject(element)
		if err != nil || !exactKeys(obj, []string{"path", "reason"}) {
			return nil, false
		}
		var path, reason string
		if err := json.Unmarshal(mustGet(obj, "path"), &path); err != nil {
			return nil, false
		}
		if err := json.Unmarshal(mustGet(obj, "reason"), &reason); err != nil {
			return nil, false
		}
		out = append(out, CoverageExclusion{Path: path, Reason: reason})
	}
	if out == nil {
		out = []CoverageExclusion{}
	}
	return out, true
}

func decodeScopeReport(raw json.RawMessage) (ScopeReport, bool) {
	obj, err := parseObject(raw)
	if err != nil || !exactKeys(obj, []string{"examined_scope", "known_limits"}) {
		return ScopeReport{}, false
	}
	var scope string
	if err := json.Unmarshal(mustGet(obj, "examined_scope"), &scope); err != nil || scope == "" {
		return ScopeReport{}, false
	}
	limits, ok := decodeStrings(mustGet(obj, "known_limits"))
	if !ok {
		return ScopeReport{}, false
	}
	return ScopeReport{ExaminedScope: scope, KnownLimits: limits}, true
}

// decodeVerification strictly decodes the reserved envelope: every declared
// member has its declared type, no member is unknown, and the status is one
// of the six declared values. The three arrays stay opaque JSON arrays — a
// scalar where an array belongs is refused, but the elements are never read.
// Populated reserved values round-trip through the codec without this release
// interpreting them as evidence.
func decodeVerification(raw json.RawMessage) (Verification, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Verification{}, false
	}
	if !exactKeys(obj, []string{"status", "revision", "checks", "sources_inspected", "rejected", "recovery"}) {
		return Verification{}, false
	}
	var v Verification
	statusRaw := mustGet(obj, "status")
	if isNull(statusRaw) {
		v.Status = Null[string]()
	} else {
		var status string
		if err := json.Unmarshal(statusRaw, &status); err != nil || !validVerificationStatus(status) {
			return Verification{}, false
		}
		v.Status = Some(status)
	}
	v.Revision = decodeOptString(mustGet(obj, "revision"))
	if !v.Revision.Present() {
		return Verification{}, false
	}
	var ok bool
	if v.Checks, ok = decodeOpaqueArray(mustGet(obj, "checks")); !ok {
		return Verification{}, false
	}
	if v.SourcesInspected, ok = decodeOpaqueArray(mustGet(obj, "sources_inspected")); !ok {
		return Verification{}, false
	}
	if v.Rejected, ok = decodeOpaqueArray(mustGet(obj, "rejected")); !ok {
		return Verification{}, false
	}
	v.Recovery = decodeOptString(mustGet(obj, "recovery"))
	if !v.Recovery.Present() {
		return Verification{}, false
	}
	return v, true
}

// validVerificationStatus reports whether s is one of the six declared
// envelope statuses. The reservation accepts every declared value on decode;
// only the writer is confined to not_implemented.
func validVerificationStatus(s string) bool {
	switch s {
	case VerificationNotImplemented,
		VerificationPassed,
		VerificationFailed,
		VerificationPending,
		VerificationUnavailable,
		VerificationNoChecksDiscovered:
		return true
	}
	return false
}

// decodeOptString reads a string|null member. Any other type refuses the
// payload: a missing nullable is data loss, not leniency.
func decodeOptString(raw json.RawMessage) Opt[string] {
	if isNull(raw) {
		return Null[string]()
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return Opt[string]{}
	}
	return Some(s)
}

// decodeOpaqueArray reads an array|null member as opaque bytes, preserving
// populated values without interpreting them. A scalar where an array belongs
// is refused. An empty array is a present empty array, not null: the writer
// test pins that this release never writes one.
func decodeOpaqueArray(raw json.RawMessage) (Opt[json.RawMessage], bool) {
	if isNull(raw) {
		return Null[json.RawMessage](), true
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return Opt[json.RawMessage]{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if _, err := dec.Token(); err != nil {
		return Opt[json.RawMessage]{}, false
	}
	var kept []json.RawMessage
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return Opt[json.RawMessage]{}, false
		}
		compact, err := compactValue(element)
		if err != nil {
			return Opt[json.RawMessage]{}, false
		}
		kept = append(kept, compact)
	}
	out := []byte{'['}
	for i, element := range kept {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, element...)
	}
	out = append(out, ']')
	return Some(json.RawMessage(out)), true
}

// isNull reports whether raw is the JSON null literal.
func isNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

func decodeRecords(raw json.RawMessage) ([]Record, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || raw[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var out []Record
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return nil, false
		}
		record, ok := decodeRecord(element)
		if !ok {
			return nil, false
		}
		out = append(out, record)
	}
	if out == nil {
		out = []Record{}
	}
	return out, true
}

// decodeRecord strictly decodes one coverage record. Outstanding records
// carry a null disposition and a null finding_ids; judged units carry both.
// Both carry the fixed file kind, one of the five changes, a 16-hex unit id
// and a 64-hex body digest.
func decodeRecord(raw json.RawMessage) (Record, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Record{}, false
	}
	if !exactKeys(obj, []string{"type", "unit_id", "path_index", "kind", "change",
		"body_digest", "disposition", "finding_ids", "evidence", "reason"}) {
		return Record{}, false
	}
	var recordType string
	if err := json.Unmarshal(mustGet(obj, "type"), &recordType); err != nil ||
		(recordType != CoverageRecordUnit && recordType != CoverageRecordOutstanding) {
		return Record{}, false
	}
	var unitID string
	if err := json.Unmarshal(mustGet(obj, "unit_id"), &unitID); err != nil || !isHex16(unitID) {
		return Record{}, false
	}
	pathIndex, ok := decodeInt(mustGet(obj, "path_index"))
	if !ok {
		return Record{}, false
	}
	var kind string
	if err := json.Unmarshal(mustGet(obj, "kind"), &kind); err != nil || kind != CoverageGranularityFile {
		return Record{}, false
	}
	var change string
	if err := json.Unmarshal(mustGet(obj, "change"), &change); err != nil || !validChange(change) {
		return Record{}, false
	}
	var bodyDigest string
	if err := json.Unmarshal(mustGet(obj, "body_digest"), &bodyDigest); err != nil || !isHex64(bodyDigest) {
		return Record{}, false
	}
	disposition := decodeOptString(mustGet(obj, "disposition"))
	if !disposition.Present() {
		return Record{}, false
	}
	if disposition.Present() && !disposition.IsNull() {
		if !validDisposition(disposition.Value()) {
			return Record{}, false
		}
	}
	var findingIDs []string
	findingRaw := mustGet(obj, "finding_ids")
	if isNull(findingRaw) {
		findingIDs = nil
	} else {
		ids, ok := decodeStrings(findingRaw)
		if !ok {
			return Record{}, false
		}
		findingIDs = ids
	}
	if recordType == CoverageRecordOutstanding {
		if !disposition.IsNull() || findingIDs != nil {
			return Record{}, false
		}
	} else if disposition.IsNull() {
		return Record{}, false
	}
	evidence, ok := decodeEvidences(mustGet(obj, "evidence"))
	if !ok {
		return Record{}, false
	}
	reason := decodeOptString(mustGet(obj, "reason"))
	if !reason.Present() {
		return Record{}, false
	}
	return Record{
		Type:        recordType,
		UnitID:      unitID,
		PathIndex:   pathIndex,
		Kind:        kind,
		Change:      change,
		BodyDigest:  bodyDigest,
		Disposition: disposition,
		FindingIDs:  findingIDs,
		Evidence:    evidence,
		Reason:      reason,
	}, true
}

func decodeEvidences(raw json.RawMessage) ([]Evidence, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || raw[0] != '[' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, false
	}
	var out []Evidence
	for dec.More() {
		var element json.RawMessage
		if err := dec.Decode(&element); err != nil {
			return nil, false
		}
		ev, ok := decodeEvidence(element)
		if !ok {
			return nil, false
		}
		out = append(out, ev)
	}
	if out == nil {
		out = []Evidence{}
	}
	return out, true
}

// decodeEvidence strictly decodes one evidence item: a non-empty path, a
// present revision, one of the four sources, and whole or null line spans.
func decodeEvidence(raw json.RawMessage) (Evidence, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Evidence{}, false
	}
	if !exactKeys(obj, []string{"path", "revision", "start_line", "end_line", "source", "note"}) {
		return Evidence{}, false
	}
	var path string
	if err := json.Unmarshal(mustGet(obj, "path"), &path); err != nil || path == "" {
		return Evidence{}, false
	}
	revision := decodeOptString(mustGet(obj, "revision"))
	if !revision.Present() {
		return Evidence{}, false
	}
	start, ok := decodeOptLine(mustGet(obj, "start_line"))
	if !ok {
		return Evidence{}, false
	}
	end, ok := decodeOptLine(mustGet(obj, "end_line"))
	if !ok {
		return Evidence{}, false
	}
	var source string
	if err := json.Unmarshal(mustGet(obj, "source"), &source); err != nil || !validSource(source) {
		return Evidence{}, false
	}
	note := decodeOptString(mustGet(obj, "note"))
	if !note.Present() {
		return Evidence{}, false
	}
	return Evidence{
		Path:      path,
		Revision:  revision,
		StartLine: start,
		EndLine:   end,
		Source:    source,
		Note:      note,
	}, true
}

// decodeOptLine reads an integer-or-null line endpoint. A fractional or
// non-positive literal refuses the payload.
func decodeOptLine(raw json.RawMessage) (Opt[int], bool) {
	if isNull(raw) {
		return Null[int](), true
	}
	n, ok := decodeInt(raw)
	if !ok || n < 1 {
		return Opt[int]{}, false
	}
	return Some(n), true
}

func validChange(s string) bool {
	switch s {
	case "added", "modified", "deleted", "renamed", "type_changed":
		return true
	}
	return false
}

func validDisposition(s string) bool {
	switch s {
	case "no_issue", "finding", "not_affected", "could_not_review":
		return true
	}
	return false
}

func validSource(s string) bool {
	switch s {
	case "git", "search", "convention", "reviewer":
		return true
	}
	return false
}

// isHex16 reports whether s is 16 lowercase hexadecimal characters, the
// width a unit identity keeps.
func isHex16(s string) bool {
	if len(s) != 16 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// isHex64 reports whether s is 64 lowercase hexadecimal characters, the
// width an integrity or body digest keeps.
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// BuildManifest assembles one complete generation the writer can encode:
// every required unit appears either as a judged record or as an
// outstanding one, the counts name the required set, and the verification
// envelope is the unimplemented one. It fails rather than writing a
// generation that mixes another engine's verification evidence into new
// bytes.
func BuildManifest(gen int, baseSHA, headSHA, engine string, paths []string, shards []ShardRef, outstanding, required int, advisory Advisory, excluded []CoverageExclusion, report ScopeReport) Manifest {
	return Manifest{
		Version:          coverageSchemaVersion,
		Kind:             CoverageKindManifest,
		Gen:              gen,
		BaseSHA:          baseSHA,
		HeadSHA:          headSHA,
		Engine:           engine,
		Granularity:      CoverageGranularityFile,
		Paths:            paths,
		Shards:           shards,
		OutstandingCount: outstanding,
		RequiredCount:    required,
		Advisory:         advisory,
		Excluded:         excluded,
		ScopeReport:      report,
		Verification:     UnimplementedVerification(),
	}
}

// BuildShard assembles one shard the writer can encode.
func BuildShard(pos int, records []Record) Shard {
	return Shard{
		Version: coverageSchemaVersion,
		Kind:    CoverageKindShard,
		Pos:     pos,
		Records: records,
	}
}

// OutstandingRecord builds one outstanding record: no disposition, null
// finding ids, and the reason the work remains.
func OutstandingRecord(unitID string, pathIndex int, change, bodyDigest, reason string) Record {
	return Record{
		Type:       CoverageRecordOutstanding,
		UnitID:     unitID,
		PathIndex:  pathIndex,
		Kind:       CoverageGranularityFile,
		Change:     change,
		BodyDigest: bodyDigest,
		Reason:     Some(reason),
		Evidence:   []Evidence{},
	}
}
