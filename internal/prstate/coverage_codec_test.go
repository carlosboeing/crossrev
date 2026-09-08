package prstate_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// mustRevision builds a 40-hex revision or fails the test.
func mustCoverageRevision(t *testing.T, sha string) core.Revision {
	t.Helper()
	rev, err := core.NewRevision(sha)
	if err != nil {
		t.Fatalf("revision %q: %v", sha, err)
	}
	return rev
}

func coverageBaseSHA() string { return "1111111111111111111111111111111111111111" }

func coverageHeadSHA() string { return "2222222222222222222222222222222222222222" }

func coverageUnitID(path string) string { return string(core.FileUnitID(path)) }

func coverageBodyDigest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// sampleManifest builds one complete generation: two required files, one
// judged and one outstanding, with advisory, exclusions, a scope report and
// the unimplemented envelope. The shard reference is filled in by the caller
// once the shard comment exists.
func sampleManifest() prstate.Manifest {
	return prstate.BuildManifest(
		7,
		coverageBaseSHA(),
		coverageHeadSHA(),
		core.FileEngineVersion,
		[]string{"a.go", "b.go"},
		nil,
		1, 2,
		prstate.Advisory{Count: 1, Rules: []string{"convention", "search"}, Limits: []prstate.AdvisoryLimit{
			{Rule: "search:SharedThing", Observed: 200, Limit: 200, Reason: "too_common"},
		}},
		[]prstate.CoverageExclusion{{Path: "vendor/lib.go", Reason: "vendored code"}},
		prstate.ScopeReport{ExaminedScope: "Read a.go and b.go at the head.", KnownLimits: []string{"b.go is binary."}},
	)
}

// sampleRecords builds one judged record and one outstanding record for the
// sample manifest's two paths.
func sampleRecords(t *testing.T) []prstate.Record {
	t.Helper()
	_ = mustCoverageRevision(t, coverageHeadSHA())
	return []prstate.Record{
		{
			Type:        prstate.CoverageRecordUnit,
			UnitID:      coverageUnitID("a.go"),
			PathIndex:   0,
			Kind:        prstate.CoverageGranularityFile,
			Change:      "modified",
			BodyDigest:  coverageBodyDigest("package a\n"),
			Disposition: prstate.Some("no_issue"),
			FindingIDs:  []string{},
			Evidence: []prstate.Evidence{{
				Path:      "a.go",
				Revision:  prstate.Some(coverageHeadSHA()),
				StartLine: prstate.Some(1),
				EndLine:   prstate.Some(1),
				Source:    "git",
				Note:      prstate.Null[string](),
			}},
			Reason: prstate.Null[string](),
		},
		prstate.OutstandingRecord(coverageUnitID("b.go"), 1, "added", coverageBodyDigest(""), "binary content is not shown"),
	}
}

// encodeSampleShard encodes the sample records and returns the body and the
// shard reference naming it.
func encodeSampleShard(t *testing.T, id int64) (string, prstate.ShardRef) {
	t.Helper()
	shard := prstate.BuildShard(0, sampleRecords(t))
	body, err := prstate.EncodeCoverageShard(shard)
	if err != nil {
		t.Fatalf("encoding a shard: %v", err)
	}
	decoded, ok := prstate.DecodeCoverageShard(body)
	if !ok {
		t.Fatalf("decoding the shard just written")
	}
	return body, prstate.ShardRef{Pos: 0, ID: id, Digest: decoded.Digest, N: len(sampleRecords(t))}
}

// encodeSampleManifest encodes the sample manifest naming the sample shard.
func encodeSampleManifest(t *testing.T, ref prstate.ShardRef) string {
	t.Helper()
	m := sampleManifest()
	m.Shards = []prstate.ShardRef{ref}
	body, err := prstate.EncodeCoverageManifest(m)
	if err != nil {
		t.Fatalf("encoding a manifest: %v", err)
	}
	return body
}

// TestCoverageCodecMatchesTheV1Schema round-trips one manifest, one shard and
// one outstanding record through the coverage marker, then checks the
// canonical byte order and the digest contract.
//
// It fails before the change because there is no coverage marker, no manifest
// or shard type, and no digest the bytes are verified against.
func TestCoverageCodecMatchesTheV1Schema(t *testing.T) {
	shardBody, ref := encodeSampleShard(t, 8812345)
	manifestBody := encodeSampleManifest(t, ref)

	m, ok := prstate.DecodeCoverageManifest(manifestBody)
	if !ok {
		t.Fatal("decoding the manifest just written")
	}
	if m.Version != 1 || m.Kind != "manifest" || m.Gen != 7 {
		t.Errorf("manifest identity is %d %q gen %d", m.Version, m.Kind, m.Gen)
	}
	if m.BaseSHA != coverageBaseSHA() || m.HeadSHA != coverageHeadSHA() {
		t.Errorf("manifest revisions are %q %q", m.BaseSHA, m.HeadSHA)
	}
	if m.Engine != core.FileEngineVersion || m.Granularity != "file" {
		t.Errorf("manifest engine is %q granularity %q", m.Engine, m.Granularity)
	}
	if len(m.Paths) != 2 || len(m.Shards) != 1 {
		t.Fatalf("manifest holds %d paths and %d shards", len(m.Paths), len(m.Shards))
	}
	if m.Shards[0] != ref {
		t.Errorf("shard reference is %+v, want %+v", m.Shards[0], ref)
	}
	if m.OutstandingCount != 1 || m.RequiredCount != 2 {
		t.Errorf("counts are outstanding %d required %d", m.OutstandingCount, m.RequiredCount)
	}
	if m.ScopeReport.ExaminedScope == "" {
		t.Error("the scope report lost the examined scope")
	}

	s, ok := prstate.DecodeCoverageShard(shardBody)
	if !ok {
		t.Fatal("decoding the shard just written")
	}
	if s.Version != 1 || s.Kind != "shard" || s.Pos != 0 {
		t.Errorf("shard identity is %d %q pos %d", s.Version, s.Kind, s.Pos)
	}
	if len(s.Records) != 2 {
		t.Fatalf("shard holds %d records", len(s.Records))
	}
	judged, outstanding := s.Records[0], s.Records[1]
	if judged.Type != "unit" || judged.Disposition.Value() != "no_issue" {
		t.Errorf("judged record is %+v", judged)
	}
	if outstanding.Type != "outstanding" || !outstanding.Disposition.IsNull() || outstanding.FindingIDs != nil {
		t.Errorf("outstanding record is %+v", outstanding)
	}

	// The manifest keys are written in the order the plan's schema lists
	// them, and byte ordering is preserved across the round trip: encoding
	// the decoded values again reproduces the manifest body byte for byte.
	again, err := prstate.EncodeCoverageManifest(m)
	if err != nil {
		t.Fatalf("re-encoding a decoded manifest: %v", err)
	}
	if again != manifestBody {
		t.Errorf("re-encoding changed the bytes\n got %s\nwant %s", again, manifestBody)
	}
	payload := strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(manifestBody), "-->"), "<!-- crossrev:c ")
	var order []string
	dec := json.NewDecoder(strings.NewReader(payload))
	tok, err := dec.Token()
	if err != nil {
		t.Fatalf("reading the manifest payload: %v", err)
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		t.Fatalf("the manifest payload is not an object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			t.Fatalf("reading a manifest key: %v", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			t.Fatalf("a manifest key is not a string")
		}
		order = append(order, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("reading a manifest value: %v", err)
		}
	}
	wantOrder := []string{"v", "kind", "gen", "base_sha", "head_sha", "engine", "granularity",
		"paths", "shards", "outstanding_count", "required_count", "advisory",
		"excluded", "scope_report", "verification", "digest"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("manifest key order is %v, want %v", order, wantOrder)
	}

	// The digest covers the manifest bytes with the digest member removed.
	stripped := manifestBody[:strings.LastIndex(manifestBody, `,"digest":"`)] + "}"
	sum := sha256.Sum256([]byte(stripped[strings.Index(stripped, "{"):]))
	if m.Digest != hex.EncodeToString(sum[:]) {
		t.Errorf("manifest digest is %q, want the digest of the bytes without it", m.Digest)
	}

	// A flipped byte refuses rather than reading as empty coverage.
	tampered := strings.Replace(manifestBody, `"gen":7`, `"gen":8`, 1)
	if _, ok := prstate.DecodeCoverageManifest(tampered); ok {
		t.Error("a manifest with a mismatched digest decoded")
	}
	tamperedShard := strings.Replace(shardBody, `"pos":0`, `"pos":1`, 1)
	if _, ok := prstate.DecodeCoverageShard(tamperedShard); ok {
		t.Error("a shard with a mismatched digest decoded")
	}
}

// TestCoveragePrefixesCannotCrossDecode proves the three markers stay apart:
// the coverage marker decodes as neither a pass marker nor a finding id, and
// neither existing marker decodes as coverage. It also proves no foreign
// payload kind can enter the schema: a progress, slot, rollup,
// clarification, dependency or compressed member is refused by strict typed
// decoding.
func TestCoveragePrefixesCannotCrossDecode(t *testing.T) {
	shardBody, ref := encodeSampleShard(t, 99)
	manifestBody := encodeSampleManifest(t, ref)

	if _, ok := prstate.DecodeMarker(manifestBody); ok {
		t.Error("a coverage manifest decoded as a pass marker")
	}
	if _, ok := prstate.DecodeMarker(shardBody); ok {
		t.Error("a coverage shard decoded as a pass marker")
	}
	if got := prstate.FindingIDs([]string{manifestBody, shardBody}, core.LegReview, 0); len(got) != 0 {
		t.Errorf("coverage bodies produced finding ids %v", got)
	}
	if _, ok := prstate.DecodeCoverageManifest("text\n\n<!-- crossrev: {\"v\":1,\"leg\":\"review\"} -->"); ok {
		t.Error("a pass marker decoded as a coverage manifest")
	}
	if _, ok := prstate.DecodeCoverageShard("text\n\n<!-- crossrev:f {\"id\":\"aaaa000000000001\",\"pass\":1,\"leg\":\"review\"} -->"); ok {
		t.Error("a finding marker decoded as a coverage shard")
	}
	if _, ok := prstate.DecodeCoverageManifest(manifestBody); !ok {
		t.Fatal("the sample manifest stopped decoding")
	}
	if _, ok := prstate.DecodeCoverageShard(manifestBody); ok {
		t.Error("a manifest decoded as a shard")
	}
	if _, ok := prstate.DecodeCoverageManifest(shardBody); ok {
		t.Error("a shard decoded as a manifest")
	}

	// No foreign member enters the schema. Each payload below is the sample
	// manifest with one extra member injected before the digest, re-digested
	// so the refusal is the strict shape and not the integrity check.
	foreign := []string{
		`"progress":[{"pos":0}]`,
		`"slot":"reviewer-1"`,
		`"rollups":[{"sc":"file"}]`,
		`"clarifications":[{"by":"carlosboeing"}]`,
		`"dependencies":[{"id":"3f8a1b2c4d5e6f70"}]`,
		`"compressed":true`,
		`"batches":[{"n":1}]`,
	}
	for _, member := range foreign {
		injected := strings.Replace(manifestBody, `,"digest":"`, ","+member+`,"digest":"`, 1)
		payload := strings.TrimPrefix(strings.TrimSpace(injected[strings.Index(injected, "<!-- crossrev:c "):]), "<!-- crossrev:c ")
		payload = strings.TrimSuffix(payload, " -->")
		stripped := payload[:strings.LastIndex(payload, `,"digest":"`)] + "}"
		sum := sha256.Sum256([]byte(stripped))
		redigested := payload[:strings.LastIndex(payload, `,"digest":"`)+len(`,"digest":"`)] +
			hex.EncodeToString(sum[:]) + `"}` + " -->"
		redigested = injected[:strings.Index(injected, "<!-- crossrev:c ")] + "<!-- crossrev:c " + redigested
		if _, ok := prstate.DecodeCoverageManifest(redigested); ok {
			t.Errorf("a manifest carrying %s decoded", member)
		}
	}

	// A mistyped member is refused as well: a string where the counts belong,
	// an object where the paths belong, and a scalar finding_ids.
	for _, tampered := range []string{
		strings.Replace(manifestBody, `"required_count":2`, `"required_count":"2"`, 1),
		strings.Replace(manifestBody, `"paths":["a.go","b.go"]`, `"paths":{"a.go":1}`, 1),
		strings.Replace(shardBody, `"finding_ids":[]`, `"finding_ids":"none"`, 1),
		strings.Replace(shardBody, `"type":"unit"`, `"type":"rollup"`, 1),
		strings.Replace(shardBody, `"kind":"file"`, `"kind":"symbol"`, 1),
		strings.Replace(manifestBody, `"granularity":"file"`, `"granularity":"symbol"`, 1),
		strings.Replace(manifestBody, `"v":1`, `"v":2`, 1),
	} {
		if _, ok := prstate.DecodeCoverageManifest(tampered); ok {
			if strings.Contains(tampered, "crossrev:c") && strings.Contains(tampered, `"kind":"manifest"`) {
				t.Errorf("a mistyped manifest decoded: %.80s", tampered)
			}
		}
		if _, ok := prstate.DecodeCoverageShard(tampered); ok {
			if strings.Contains(tampered, `"kind":"shard"`) {
				t.Errorf("a mistyped shard decoded: %.80s", tampered)
			}
		}
	}
}

// TestReservedVerificationEnvelopeRoundTrips proves the envelope reserves the
// future values without interpreting them: null and populated reserved arrays
// and every declared status round-trip through decode and re-encode, and a
// scalar where an array belongs is refused.
func TestReservedVerificationEnvelopeRoundTrips(t *testing.T) {
	_, ref := encodeSampleShard(t, 7)
	base := sampleManifest()
	base.Shards = []prstate.ShardRef{ref}

	// The writer's own envelope round-trips.
	body, err := prstate.EncodeCoverageManifest(base)
	if err != nil {
		t.Fatalf("encoding a manifest: %v", err)
	}
	m, ok := prstate.DecodeCoverageManifest(body)
	if !ok {
		t.Fatal("decoding the manifest just written")
	}
	if m.Verification.Status.Value() != prstate.VerificationNotImplemented {
		t.Errorf("writer envelope status is %q", m.Verification.Status.Value())
	}
	again, err := prstate.EncodeCoverageManifest(m)
	if err != nil {
		t.Fatalf("re-encoding a decoded manifest: %v", err)
	}
	if again != body {
		t.Errorf("re-encoding changed the bytes\n got %s\nwant %s", again, body)
	}

	// Every declared status decodes and re-encodes, with populated reserved
	// arrays preserved opaquely rather than interpreted.
	for _, status := range prstate.VerificationStatuses() {
		withStatus := strings.Replace(body, `"status":"not_implemented"`, `"status":"`+status+`"`, 1)
		populated := withStatus
		for _, member := range []string{
			`"checks":null`,
			`"sources_inspected":null`,
			`"rejected":null`,
		} {
			var replacement string
			switch member {
			case `"checks":null`:
				replacement = `"checks":[{"id":"test","revision":"` + coverageHeadSHA() + `"}]`
			case `"sources_inspected":null`:
				replacement = `"sources_inspected":[{"path":".github/workflows/test.yml","revision":"base"}]`
			case `"rejected":null`:
				replacement = `"rejected":[{"check_id":"other","reason":"unselected"}]`
			}
			populated = strings.Replace(populated, member, replacement, 1)
		}
		populated = strings.Replace(populated, `"revision":null`, `"revision":"`+coverageHeadSHA()+`"`, 1)
		populated = strings.Replace(populated, `"recovery":null`, `"recovery":"push a commit first"`, 1)
		payload := strings.TrimPrefix(strings.TrimSpace(populated[strings.Index(populated, "<!-- crossrev:c "):]), "<!-- crossrev:c ")
		payload = strings.TrimSuffix(payload, " -->")
		stripped := payload[:strings.LastIndex(payload, `,"digest":"`)] + "}"
		sum := sha256.Sum256([]byte(stripped))
		redigested := populated[:strings.LastIndex(populated, `,"digest":"`)+len(`,"digest":"`)] +
			hex.EncodeToString(sum[:]) + `"}` + " -->"
		got, ok := prstate.DecodeCoverageManifest(redigested)
		if !ok {
			t.Errorf("status %q with populated reserved values refused", status)
			continue
		}
		if got.Verification.Status.Value() != status {
			t.Errorf("status is %q, want %q", got.Verification.Status.Value(), status)
		}
		if got.Verification.Revision.Value() != coverageHeadSHA() {
			t.Errorf("revision is %q", got.Verification.Revision.Value())
		}
		for _, field := range []struct {
			name string
			raw  json.RawMessage
		}{
			{"checks", got.Verification.Checks.Value()},
			{"sources_inspected", got.Verification.SourcesInspected.Value()},
			{"rejected", got.Verification.Rejected.Value()},
		} {
			var elements []json.RawMessage
			if err := json.Unmarshal(field.raw, &elements); err != nil || len(elements) != 1 {
				t.Errorf("status %q: %s did not round-trip as one opaque element: %s", status, field.name, field.raw)
			}
		}
		reencoded, err := prstate.EncodeCoverageManifest(got)
		if err != nil {
			t.Fatalf("re-encoding status %q: %v", status, err)
		}
		if reencoded != redigested {
			t.Errorf("status %q: re-encoding changed the bytes\n got %s\nwant %s", status, reencoded, redigested)
		}
	}

	// An undeclared status is refused.
	undeclared := strings.Replace(body, `"status":"not_implemented"`, `"status":"verified"`, 1)
	if _, ok := prstate.DecodeCoverageManifest(undeclared); ok {
		t.Error("an undeclared verification status decoded")
	}

	// A scalar where an array belongs is refused, whichever reserved array
	// carries it.
	for _, member := range []string{`"checks":null`, `"sources_inspected":null`, `"rejected":null`} {
		scalar := strings.Replace(body, member, strings.TrimSuffix(member, "null")+`"none"`, 1)
		if _, ok := prstate.DecodeCoverageManifest(scalar); ok {
			t.Errorf("a scalar %s decoded", member)
		}
		scalarNumber := strings.Replace(body, member, strings.TrimSuffix(member, "null")+`7`, 1)
		if _, ok := prstate.DecodeCoverageManifest(scalarNumber); ok {
			t.Errorf("a numeric %s decoded", member)
		}
	}
}

// TestCoverageWriterEmitsOnlyNotImplemented requires that this release's own
// new generations write not_implemented and five nulls. It fails if the
// writer claims no_checks_discovered, writes an empty result array, or copies
// another engine's verification evidence into a new generation.
func TestCoverageWriterEmitsOnlyNotImplemented(t *testing.T) {
	_, ref := encodeSampleShard(t, 11)
	m := sampleManifest()
	m.Shards = []prstate.ShardRef{ref}
	body, err := prstate.EncodeCoverageManifest(m)
	if err != nil {
		t.Fatalf("encoding a manifest: %v", err)
	}
	for _, want := range []string{
		`"status":"not_implemented"`,
		`"revision":null`,
		`"checks":null`,
		`"sources_inspected":null`,
		`"rejected":null`,
		`"recovery":null`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("writer envelope misses %s:\n%s", want, body)
		}
	}

	decoded, ok := prstate.DecodeCoverageManifest(body)
	if !ok {
		t.Fatal("decoding the manifest just written")
	}
	if decoded.Verification.Status.Value() != prstate.VerificationNotImplemented {
		t.Errorf("writer status is %q", decoded.Verification.Status.Value())
	}

	// The constructor emits only the unimplemented envelope: there is no
	// argument that smuggles another engine's evidence into a new
	// generation, and re-encoding a decoded foreign envelope does not launder
	// it into this release's output — BuildManifest always starts from
	// UnimplementedVerification.
	fresh := prstate.BuildManifest(8, coverageBaseSHA(), coverageHeadSHA(), core.FileEngineVersion,
		[]string{"a.go"}, []prstate.ShardRef{ref}, 0, 1, prstate.Advisory{},
		nil, prstate.ScopeReport{ExaminedScope: "Read a.go.", KnownLimits: []string{}})
	freshBody, err := prstate.EncodeCoverageManifest(fresh)
	if err != nil {
		t.Fatalf("encoding a fresh manifest: %v", err)
	}
	for _, forbidden := range []string{
		`"status":"no_checks_discovered"`,
		`"status":"passed"`,
		`"checks":[]`,
		`"checks":[`,
		`"sources_inspected":[`,
		`"rejected":[`,
	} {
		if strings.Contains(freshBody, forbidden) {
			t.Errorf("a fresh generation carries %q:\n%s", forbidden, freshBody)
		}
	}
}
