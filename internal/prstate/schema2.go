package prstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/core"
)

// CoverageSchemaV2 is the v2 schema version for ref-store and marker-store
// coverage generations.
const CoverageSchemaV2 = 2

// Supplied input forms.
const (
	SuppliedFormFullText = "full_text"
	SuppliedFormDiffOnly = "diff_only"
)

// CoverageKindRecords is the kind of records payload in v2.
const CoverageKindRecords = "records"

// SuppliedInput is what the reviewer was actually given for one file:
// measured at prompt-assembly time, never reported by the model.
type SuppliedInput struct {
	Digest    string `json:"digest"` // sha256 over the exact bytes handed over
	Form      string `json:"form"`   // full_text | diff_only
	Truncated bool   `json:"truncated"`
}

// Reaction is the reserved human-reaction envelope. Every member is null in
// this release. Populated values round-trip byte for byte and are never read
// as evidence, exactly as Verification does.
type Reaction struct {
	Reactions     Opt[json.RawMessage] `json:"reactions,omitzero"`
	ThreadState   Opt[string]          `json:"thread_state,omitzero"`
	HumanReplied  Opt[bool]            `json:"human_replied,omitzero"`
	AnchorChanged Opt[bool]            `json:"anchor_changed,omitzero"`
}

// UnimplementedReaction returns the reserved reaction envelope with every
// member null.
func UnimplementedReaction() Reaction {
	return Reaction{
		Reactions:     Null[json.RawMessage](),
		ThreadState:   Null[string](),
		HumanReplied:  Null[bool](),
		AnchorChanged: Null[bool](),
	}
}

func validSuppliedForm(s string) bool {
	return s == SuppliedFormFullText || s == SuppliedFormDiffOnly
}

func verdictCode(r Record) (byte, error) {
	if r.Type == CoverageRecordOutstanding || !r.Verdict.Present() || r.Verdict.IsNull() {
		return '0', nil
	}
	switch r.Verdict.Value() {
	case "no_issue":
		return '1', nil
	case "finding":
		return '2', nil
	case "not_affected":
		return '3', nil
	case "could_not_review":
		return '4', nil
	default:
		return 0, coverageErrorf("unknown verdict %q", r.Verdict.Value())
	}
}

func recordFromVerdictCode(code byte, pathIndex int, path string) (Record, error) {
	// The unit id is re-derived, not stored: FileUnitID is a pure function
	// of the path, so the compact wire form carries no identity and a
	// decoded compact record still resumes exactly where a full one would.
	unitID := string(core.FileUnitID(path))
	switch code {
	case '0':
		return Record{
			Type:      CoverageRecordOutstanding,
			UnitID:    unitID,
			PathIndex: pathIndex,
			Kind:      CoverageGranularityFile,
			Verdict:   Null[string](),
		}, nil
	case '1':
		return Record{
			Type:      CoverageRecordUnit,
			UnitID:    unitID,
			PathIndex: pathIndex,
			Kind:      CoverageGranularityFile,
			Verdict:   Some("no_issue"),
		}, nil
	case '2':
		return Record{
			Type:      CoverageRecordUnit,
			UnitID:    unitID,
			PathIndex: pathIndex,
			Kind:      CoverageGranularityFile,
			Verdict:   Some("finding"),
		}, nil
	case '3':
		return Record{
			Type:      CoverageRecordUnit,
			UnitID:    unitID,
			PathIndex: pathIndex,
			Kind:      CoverageGranularityFile,
			Verdict:   Some("not_affected"),
		}, nil
	case '4':
		return Record{
			Type:      CoverageRecordUnit,
			UnitID:    unitID,
			PathIndex: pathIndex,
			Kind:      CoverageGranularityFile,
			Verdict:   Some("could_not_review"),
		}, nil
	default:
		return Record{}, coverageErrorf("unknown verdict code %q", string(code))
	}
}

// CompactGeneration reduces every record to its verdict code, keeping the
// envelope whole. Progress survives; detail does not.
func CompactGeneration(g Generation) Generation {
	byIndex := make(map[int]Record, len(g.Records))
	for _, r := range g.Records {
		byIndex[r.PathIndex] = r
	}
	compactRecords := make([]Record, len(g.Paths))
	for i := range g.Paths {
		r, ok := byIndex[i]
		if !ok {
			compactRecords[i] = Record{
				Type:      CoverageRecordOutstanding,
				UnitID:    string(core.FileUnitID(g.Paths[i])),
				PathIndex: i,
				Kind:      CoverageGranularityFile,
				Verdict:   Null[string](),
			}
			continue
		}
		code, err := verdictCode(r)
		if err != nil {
			code = '0'
		}
		rec, _ := recordFromVerdictCode(code, i, g.Paths[i])
		compactRecords[i] = rec
	}
	var paths []string
	if g.Paths != nil {
		paths = append([]string{}, g.Paths...)
	}
	var excluded []CoverageExclusion
	if g.Excluded != nil {
		excluded = append([]CoverageExclusion{}, g.Excluded...)
	}
	return Generation{
		Gen:         g.Gen,
		Revision:    g.Revision,
		Engine:      g.Engine,
		Slot:        g.Slot,
		Producer:    g.Producer,
		Form:        GenerationCompact,
		Paths:       paths,
		Records:     compactRecords,
		Advisory:    g.Advisory,
		Excluded:    excluded,
		ScopeReport: g.ScopeReport,
	}
}

func optBoolOf(o Opt[bool]) json.RawMessage {
	if !o.Present() || o.IsNull() {
		return json.RawMessage("null")
	}
	if o.Value() {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

func suppliedOf(s Opt[SuppliedInput]) json.RawMessage {
	if !s.Present() || s.IsNull() {
		return json.RawMessage("null")
	}
	v := s.Value()
	trunc := "false"
	if v.Truncated {
		trunc = "true"
	}
	return object{
		{key: "digest", value: appendJSONString(nil, v.Digest)},
		{key: "form", value: appendJSONString(nil, v.Form)},
		{key: "truncated", value: json.RawMessage(trunc)},
	}.marshal()
}

func reactionOf(r Reaction) json.RawMessage {
	return object{
		{key: "reactions", value: optRawOf(r.Reactions)},
		{key: "thread_state", value: optStringOf(r.ThreadState)},
		{key: "human_replied", value: optBoolOf(r.HumanReplied)},
		{key: "anchor_changed", value: optBoolOf(r.AnchorChanged)},
	}.marshal()
}

func producerOf(p Producer) json.RawMessage {
	return object{
		{key: "harness", value: appendJSONString(nil, p.Harness)},
		{key: "model", value: appendJSONString(nil, p.Model)},
		{key: "effort", value: appendJSONString(nil, p.Effort)},
		{key: "endpoint", value: appendJSONString(nil, p.Endpoint)},
	}.marshal()
}

func recordOfV2(r Record) json.RawMessage {
	obj := object{
		{key: "type", value: appendJSONString(nil, r.Type)},
		{key: "unit_id", value: appendJSONString(nil, r.UnitID)},
		{key: "path_index", value: json.RawMessage(fmt.Sprintf("%d", r.PathIndex))},
		{key: "kind", value: appendJSONString(nil, r.Kind)},
		{key: "change", value: appendJSONString(nil, r.Change)},
		{key: "body_digest", value: appendJSONString(nil, r.BodyDigest)},
		{key: "verdict", value: optStringOf(r.Verdict)},
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
	obj = append(obj,
		member{key: "reason", value: optStringOf(r.Reason)},
		member{key: "supplied", value: suppliedOf(r.Supplied)},
		member{key: "reaction", value: reactionOf(r.Reaction)},
	)
	return obj.marshal()
}

func recordsOfV2(in []Record) json.RawMessage {
	out := []byte{'['}
	for i, r := range in {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, recordOfV2(r)...)
	}
	return append(out, ']')
}

func generationCounts(g Generation) (outstanding, required int) {
	required = len(g.Paths)
	if len(g.Records) > required {
		required = len(g.Records)
	}
	for _, r := range g.Records {
		if r.Type == CoverageRecordOutstanding || !r.Verdict.Present() || r.Verdict.IsNull() || r.Verdict.Value() == "outstanding" {
			outstanding++
		}
	}
	return outstanding, required
}

func manifestFieldsV2(g Generation, recordsDigest, manifestDigest string) object {
	outstanding, required := generationCounts(g)
	return object{
		{key: "v", value: json.RawMessage(fmt.Sprintf("%d", CoverageSchemaV2))},
		{key: "kind", value: appendJSONString(nil, CoverageKindManifest)},
		{key: "gen", value: json.RawMessage(fmt.Sprintf("%d", g.Gen))},
		{key: "base_sha", value: appendJSONString(nil, g.Revision.Base.SHA())},
		{key: "head_sha", value: appendJSONString(nil, g.Revision.Head.SHA())},
		{key: "engine", value: appendJSONString(nil, g.Engine)},
		{key: "slot", value: appendJSONString(nil, g.Slot)},
		{key: "producer", value: producerOf(g.Producer)},
		{key: "form", value: appendJSONString(nil, g.Form)},
		{key: "granularity", value: appendJSONString(nil, CoverageGranularityFile)},
		{key: "paths", value: stringsOf(g.Paths)},
		{key: "outstanding_count", value: json.RawMessage(fmt.Sprintf("%d", outstanding))},
		{key: "required_count", value: json.RawMessage(fmt.Sprintf("%d", required))},
		{key: "advisory", value: advisoryOf(g.Advisory)},
		{key: "excluded", value: exclusionsOf(g.Excluded)},
		{key: "scope_report", value: scopeReportOf(g.ScopeReport)},
		{key: "verification", value: verificationOf(UnimplementedVerification())},
		{key: "records_digest", value: appendJSONString(nil, recordsDigest)},
		{key: "digest", value: appendJSONString(nil, manifestDigest)},
	}
}

func compactRecordsFields(verdicts, digest string) object {
	return object{
		{key: "v", value: json.RawMessage(fmt.Sprintf("%d", CoverageSchemaV2))},
		{key: "kind", value: appendJSONString(nil, CoverageKindRecords)},
		{key: "form", value: appendJSONString(nil, GenerationCompact)},
		{key: "verdicts", value: appendJSONString(nil, verdicts)},
		{key: "digest", value: appendJSONString(nil, digest)},
	}
}

func fullRecordsFields(records []Record, digest string) object {
	return object{
		{key: "v", value: json.RawMessage(fmt.Sprintf("%d", CoverageSchemaV2))},
		{key: "kind", value: appendJSONString(nil, CoverageKindRecords)},
		{key: "form", value: appendJSONString(nil, GenerationFull)},
		{key: "records", value: recordsOfV2(records)},
		{key: "digest", value: appendJSONString(nil, digest)},
	}
}

// EncodeGenerationV2 returns the two blobs a generation is stored as. The
// manifest carries the digest of the records bytes, which binds the pair;
// the commit or the marker binds them again by holding both.
func EncodeGenerationV2(g Generation) (manifest, records []byte, err error) {
	form := g.Form
	if form == "" {
		form = GenerationFull
	}
	if form != GenerationFull && form != GenerationCompact {
		return nil, nil, coverageErrorf("unknown generation form %q", form)
	}

	var recObj object
	if form == GenerationCompact {
		byIndex := make(map[int]Record, len(g.Records))
		for _, r := range g.Records {
			byIndex[r.PathIndex] = r
		}
		var buf strings.Builder
		buf.Grow(len(g.Paths))
		for i := range g.Paths {
			r, ok := byIndex[i]
			if !ok {
				buf.WriteByte('0')
				continue
			}
			code, err := verdictCode(r)
			if err != nil {
				return nil, nil, err
			}
			buf.WriteByte(code)
		}
		verdicts := buf.String()
		recObj = compactRecordsFields(verdicts, "")
	} else {
		recObj = fullRecordsFields(g.Records, "")
	}

	rawRecords := json.RawMessage(recObj.marshal())
	recDigest, err := digestWithoutField(rawRecords, "digest")
	if err != nil {
		return nil, nil, coverageErrorf("computing records digest: %v", err)
	}

	if form == GenerationCompact {
		verdictsRaw, _ := recObj.get("verdicts")
		var vStr string
		_ = json.Unmarshal(verdictsRaw, &vStr)
		recObj = compactRecordsFields(vStr, recDigest)
	} else {
		recObj = fullRecordsFields(g.Records, recDigest)
	}

	normalisedRecords, err := normalise(json.RawMessage(recObj.marshal()))
	if err != nil {
		return nil, nil, coverageErrorf("normalising records: %v", err)
	}

	sum := sha256.Sum256(normalisedRecords)
	manifestRecordsDigest := hex.EncodeToString(sum[:])

	gCopy := g
	gCopy.Form = form
	manObj := manifestFieldsV2(gCopy, manifestRecordsDigest, "")
	rawManifest := json.RawMessage(manObj.marshal())
	manDigest, err := digestWithoutField(rawManifest, "digest")
	if err != nil {
		return nil, nil, coverageErrorf("computing manifest digest: %v", err)
	}

	manObj = manifestFieldsV2(gCopy, manifestRecordsDigest, manDigest)
	normalisedManifest, err := normalise(json.RawMessage(manObj.marshal()))
	if err != nil {
		return nil, nil, coverageErrorf("normalising manifest: %v", err)
	}

	return normalisedManifest, normalisedRecords, nil
}

// DecodeGenerationV2 reads the pair back, refusing a digest mismatch, an
// unknown schema version, an unknown form, a verdict string whose length
// does not match the path table, and any record whose key set is not exactly
// the one its form declares.
func DecodeGenerationV2(manifestBytes, recordsBytes []byte) (Generation, error) {
	manObj, err := parseObject(manifestBytes)
	if err != nil {
		return Generation{}, coverageErrorf("malformed manifest JSON: %v", err)
	}

	wantManifestKeys := []string{
		"v", "kind", "gen", "base_sha", "head_sha", "engine", "slot", "producer",
		"form", "granularity", "paths", "outstanding_count", "required_count",
		"advisory", "excluded", "scope_report", "verification", "records_digest", "digest",
	}
	if !exactKeys(manObj, wantManifestKeys) {
		return Generation{}, coverageErrorf("manifest keys do not match v2 schema")
	}

	getMan := func(key string) json.RawMessage {
		v, _ := manObj.get(key)
		return v
	}

	version, ok := decodeInt(getMan("v"))
	if !ok || version != CoverageSchemaV2 {
		return Generation{}, coverageErrorf("manifest schema version %d is not v2", version)
	}

	var kind string
	if err := json.Unmarshal(getMan("kind"), &kind); err != nil || kind != CoverageKindManifest {
		return Generation{}, coverageErrorf("manifest kind is not %q", CoverageKindManifest)
	}

	gen, ok := decodeInt(getMan("gen"))
	if !ok {
		return Generation{}, coverageErrorf("invalid manifest gen")
	}

	var baseSHA, headSHA, engine, slot, form, granularity, recordsDigest, digest string
	if err := json.Unmarshal(getMan("base_sha"), &baseSHA); err != nil || baseSHA == "" {
		return Generation{}, coverageErrorf("invalid base_sha")
	}
	baseRev, err := core.NewRevision(baseSHA)
	if err != nil {
		return Generation{}, coverageErrorf("invalid base_sha revision: %v", err)
	}

	if err := json.Unmarshal(getMan("head_sha"), &headSHA); err != nil || headSHA == "" {
		return Generation{}, coverageErrorf("invalid head_sha")
	}
	headRev, err := core.NewRevision(headSHA)
	if err != nil {
		return Generation{}, coverageErrorf("invalid head_sha revision: %v", err)
	}

	if err := json.Unmarshal(getMan("engine"), &engine); err != nil {
		return Generation{}, coverageErrorf("invalid engine")
	}

	if err := json.Unmarshal(getMan("slot"), &slot); err != nil {
		return Generation{}, coverageErrorf("invalid slot")
	}

	producer, ok := decodeProducer(getMan("producer"))
	if !ok {
		return Generation{}, coverageErrorf("invalid producer")
	}

	if err := json.Unmarshal(getMan("form"), &form); err != nil || (form != GenerationFull && form != GenerationCompact) {
		return Generation{}, coverageErrorf("invalid form %q", form)
	}

	if err := json.Unmarshal(getMan("granularity"), &granularity); err != nil || granularity != CoverageGranularityFile {
		return Generation{}, coverageErrorf("invalid granularity %q", granularity)
	}

	paths, ok := decodeStrings(getMan("paths"))
	if !ok {
		return Generation{}, coverageErrorf("invalid paths")
	}

	outstandingCount, ok := decodeInt(getMan("outstanding_count"))
	if !ok {
		return Generation{}, coverageErrorf("invalid outstanding_count")
	}
	_ = outstandingCount

	requiredCount, ok := decodeInt(getMan("required_count"))
	if !ok {
		return Generation{}, coverageErrorf("invalid required_count")
	}
	_ = requiredCount

	advisory, ok := decodeAdvisory(getMan("advisory"))
	if !ok {
		return Generation{}, coverageErrorf("invalid advisory")
	}

	excluded, ok := decodeExclusions(getMan("excluded"))
	if !ok {
		return Generation{}, coverageErrorf("invalid excluded")
	}

	scopeReport, ok := decodeScopeReport(getMan("scope_report"))
	if !ok {
		return Generation{}, coverageErrorf("invalid scope_report")
	}

	verification, ok := decodeVerification(getMan("verification"))
	if !ok {
		return Generation{}, coverageErrorf("invalid verification")
	}
	_ = verification

	if err := json.Unmarshal(getMan("records_digest"), &recordsDigest); err != nil || !isHex64(recordsDigest) {
		return Generation{}, coverageErrorf("invalid records_digest")
	}

	if err := json.Unmarshal(getMan("digest"), &digest); err != nil || !isHex64(digest) {
		return Generation{}, coverageErrorf("invalid digest")
	}

	if !verifyDigest(manifestBytes, digest) {
		return Generation{}, coverageErrorf("manifest digest mismatch")
	}

	actualRecordsSum := sha256.Sum256(recordsBytes)
	if hex.EncodeToString(actualRecordsSum[:]) != recordsDigest {
		return Generation{}, coverageErrorf("records digest mismatch with manifest")
	}

	recObj, err := parseObject(recordsBytes)
	if err != nil {
		return Generation{}, coverageErrorf("malformed records JSON: %v", err)
	}

	getRec := func(key string) json.RawMessage {
		v, _ := recObj.get(key)
		return v
	}

	recVersion, ok := decodeInt(getRec("v"))
	if !ok || recVersion != CoverageSchemaV2 {
		return Generation{}, coverageErrorf("records version %d is not v2", recVersion)
	}

	var recKind, recForm, recDigest string
	if err := json.Unmarshal(getRec("kind"), &recKind); err != nil || recKind != CoverageKindRecords {
		return Generation{}, coverageErrorf("records kind is not %q", CoverageKindRecords)
	}

	if err := json.Unmarshal(getRec("form"), &recForm); err != nil || recForm != form {
		return Generation{}, coverageErrorf("records form %q does not match manifest form %q", recForm, form)
	}

	if err := json.Unmarshal(getRec("digest"), &recDigest); err != nil || !isHex64(recDigest) {
		return Generation{}, coverageErrorf("invalid records self digest")
	}

	if !verifyDigest(recordsBytes, recDigest) {
		return Generation{}, coverageErrorf("records self digest mismatch")
	}

	var records []Record
	if form == GenerationCompact {
		wantCompactKeys := []string{"v", "kind", "form", "verdicts", "digest"}
		if !exactKeys(recObj, wantCompactKeys) {
			return Generation{}, coverageErrorf("compact records keys do not match schema")
		}

		var verdicts string
		if err := json.Unmarshal(getRec("verdicts"), &verdicts); err != nil {
			return Generation{}, coverageErrorf("invalid verdicts string: %v", err)
		}

		if len(verdicts) != len(paths) {
			return Generation{}, coverageErrorf("verdicts string length %d does not match paths count %d", len(verdicts), len(paths))
		}

		records = make([]Record, len(verdicts))
		for i := 0; i < len(verdicts); i++ {
			rec, err := recordFromVerdictCode(verdicts[i], i, paths[i])
			if err != nil {
				return Generation{}, err
			}
			records[i] = rec
		}
	} else {
		wantFullKeys := []string{"v", "kind", "form", "records", "digest"}
		if !exactKeys(recObj, wantFullKeys) {
			return Generation{}, coverageErrorf("full records keys do not match schema")
		}

		recs, ok := decodeRecordsV2(getRec("records"))
		if !ok {
			return Generation{}, coverageErrorf("invalid records array")
		}
		records = recs
	}

	return Generation{
		Gen:         gen,
		Revision:    core.RevisionPair{Base: baseRev, Head: headRev},
		Engine:      engine,
		Slot:        slot,
		Producer:    producer,
		Form:        form,
		Paths:       paths,
		Records:     records,
		Advisory:    advisory,
		Excluded:    excluded,
		ScopeReport: scopeReport,
	}, nil
}

func decodeRecordsV2(raw json.RawMessage) ([]Record, bool) {
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
		record, ok := decodeRecordV2(element)
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

func decodeRecordV2(raw json.RawMessage) (Record, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Record{}, false
	}
	wantKeys := []string{
		"type", "unit_id", "path_index", "kind", "change",
		"body_digest", "verdict", "finding_ids", "evidence", "reason",
		"supplied", "reaction",
	}
	if !exactKeys(obj, wantKeys) {
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

	verdict := decodeOptString(mustGet(obj, "verdict"))
	if !verdict.Present() {
		return Record{}, false
	}
	if verdict.Present() && !verdict.IsNull() {
		if !validVerdict(verdict.Value()) {
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

	evidence, ok := decodeEvidences(mustGet(obj, "evidence"))
	if !ok {
		return Record{}, false
	}

	reason := decodeOptString(mustGet(obj, "reason"))
	if !reason.Present() {
		return Record{}, false
	}

	supplied, ok := decodeSuppliedInput(mustGet(obj, "supplied"))
	if !ok {
		return Record{}, false
	}

	reaction, ok := decodeReaction(mustGet(obj, "reaction"))
	if !ok {
		return Record{}, false
	}

	if recordType == CoverageRecordOutstanding {
		if !verdict.IsNull() || findingIDs != nil || !supplied.IsNull() {
			return Record{}, false
		}
	} else if verdict.IsNull() {
		return Record{}, false
	}

	return Record{
		Type:       recordType,
		UnitID:     unitID,
		PathIndex:  pathIndex,
		Kind:       kind,
		Change:     change,
		BodyDigest: bodyDigest,
		Verdict:    verdict,
		FindingIDs: findingIDs,
		Evidence:   evidence,
		Reason:     reason,
		Supplied:   supplied,
		Reaction:   reaction,
	}, true
}

func decodeSuppliedInput(raw json.RawMessage) (Opt[SuppliedInput], bool) {
	if isNull(raw) {
		return Null[SuppliedInput](), true
	}
	obj, err := parseObject(raw)
	if err != nil {
		return Opt[SuppliedInput]{}, false
	}
	if !exactKeys(obj, []string{"digest", "form", "truncated"}) {
		return Opt[SuppliedInput]{}, false
	}
	var digest, form string
	if err := json.Unmarshal(mustGet(obj, "digest"), &digest); err != nil || !isHex64(digest) {
		return Opt[SuppliedInput]{}, false
	}
	if err := json.Unmarshal(mustGet(obj, "form"), &form); err != nil || !validSuppliedForm(form) {
		return Opt[SuppliedInput]{}, false
	}
	rawTrunc := mustGet(obj, "truncated")
	if isNull(rawTrunc) {
		return Opt[SuppliedInput]{}, false
	}
	var trunc bool
	if err := json.Unmarshal(rawTrunc, &trunc); err != nil {
		return Opt[SuppliedInput]{}, false
	}
	return Some(SuppliedInput{Digest: digest, Form: form, Truncated: trunc}), true
}

func decodeOptBool(raw json.RawMessage) Opt[bool] {
	if isNull(raw) {
		return Null[bool]()
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return Opt[bool]{}
	}
	return Some(b)
}

func decodeOptRaw(raw json.RawMessage) (Opt[json.RawMessage], bool) {
	if isNull(raw) {
		return Null[json.RawMessage](), true
	}
	compact, err := compactValue(raw)
	if err != nil {
		return Opt[json.RawMessage]{}, false
	}
	return Some(compact), true
}

func decodeReaction(raw json.RawMessage) (Reaction, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Reaction{}, false
	}
	if !exactKeys(obj, []string{"reactions", "thread_state", "human_replied", "anchor_changed"}) {
		return Reaction{}, false
	}
	var r Reaction
	reactions, ok := decodeOptRaw(mustGet(obj, "reactions"))
	if !ok {
		return Reaction{}, false
	}
	r.Reactions = reactions

	threadState := decodeOptString(mustGet(obj, "thread_state"))
	if !threadState.Present() {
		return Reaction{}, false
	}
	r.ThreadState = threadState

	humanReplied := decodeOptBool(mustGet(obj, "human_replied"))
	if !humanReplied.Present() {
		return Reaction{}, false
	}
	r.HumanReplied = humanReplied

	anchorChanged := decodeOptBool(mustGet(obj, "anchor_changed"))
	if !anchorChanged.Present() {
		return Reaction{}, false
	}
	r.AnchorChanged = anchorChanged

	return r, true
}

func decodeProducer(raw json.RawMessage) (Producer, bool) {
	obj, err := parseObject(raw)
	if err != nil {
		return Producer{}, false
	}
	if !exactKeys(obj, []string{"harness", "model", "effort", "endpoint"}) {
		return Producer{}, false
	}
	var p Producer
	if err := json.Unmarshal(mustGet(obj, "harness"), &p.Harness); err != nil {
		return Producer{}, false
	}
	if err := json.Unmarshal(mustGet(obj, "model"), &p.Model); err != nil {
		return Producer{}, false
	}
	if err := json.Unmarshal(mustGet(obj, "effort"), &p.Effort); err != nil {
		return Producer{}, false
	}
	if err := json.Unmarshal(mustGet(obj, "endpoint"), &p.Endpoint); err != nil {
		return Producer{}, false
	}
	return p, true
}
