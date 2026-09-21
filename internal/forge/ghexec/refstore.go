package ghexec

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/carlosboeing/crossrev/internal/exec"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// RefLedger returns a LedgerStore that stores and reads coverage generations
// under namespace in the git ref hierarchy using the GitHub git object API.
func (c *Client) RefLedger(namespace string) prstate.LedgerStore {
	return &refStore{
		client:    c,
		namespace: namespace,
	}
}

type refStore struct {
	client    *Client
	namespace string
}

var _ prstate.LedgerStore = (*refStore)(nil)

func (s *refStore) PublishGeneration(ctx context.Context, ref prstate.SlotRef, parent prstate.Handle, candidate prstate.Generation) (prstate.Handle, error) {
	if err := ctx.Err(); err != nil {
		return prstate.Handle{}, err
	}

	if err := prstate.ValidNamespace(s.namespace); err != nil {
		return prstate.Handle{}, fmt.Errorf("invalid namespace: %w", err)
	}
	refName, err := prstate.RefName(s.namespace, ref)
	if err != nil {
		return prstate.Handle{}, fmt.Errorf("invalid ref name: %w", err)
	}

	if s.client.filter == nil {
		return prstate.Handle{}, errNoFilter
	}

	// 1. Filter before digesting; a filter failure aborts the write.
	candCopy := candidate
	if err := prstate.FilterGeneration(s.client.filter.Filter, &candCopy); err != nil {
		return prstate.Handle{}, fmt.Errorf("filtering generation: %w", err)
	}

	genNum := candCopy.Gen
	if genNum <= 0 {
		if parent.Gen > 0 {
			genNum = parent.Gen + 1
		} else {
			genNum = 1
		}
		candCopy.Gen = genNum
	}

	// 2. Encode generation v2, compute digests
	manifestBytes, recordsBytes, err := prstate.EncodeGenerationV2(candCopy)
	if err != nil {
		return prstate.Handle{}, err
	}

	// 3. POST /git/blobs for manifest.json and records.json via runInput
	manifestBlobReq, err := json.Marshal(map[string]string{
		"content":  string(manifestBytes),
		"encoding": "utf-8",
	})
	if err != nil {
		return prstate.Handle{}, err
	}
	res := s.client.runInput(ctx, manifestBlobReq, "api", "--method", "POST",
		fmt.Sprintf("repos/%s/git/blobs", ref.Repo.String()), "--input", "-")
	if !answered(res) {
		return prstate.Handle{}, publishFailure("creating manifest blob", res)
	}
	var manifestBlobResp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(res.Stdout, &manifestBlobResp); err != nil || manifestBlobResp.SHA == "" {
		return prstate.Handle{}, fmt.Errorf("parsing manifest blob response: %w", err)
	}

	recordsBlobReq, err := json.Marshal(map[string]string{
		"content":  string(recordsBytes),
		"encoding": "utf-8",
	})
	if err != nil {
		return prstate.Handle{}, err
	}
	res = s.client.runInput(ctx, recordsBlobReq, "api", "--method", "POST",
		fmt.Sprintf("repos/%s/git/blobs", ref.Repo.String()), "--input", "-")
	if !answered(res) {
		return prstate.Handle{}, publishFailure("creating records blob", res)
	}
	var recordsBlobResp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(res.Stdout, &recordsBlobResp); err != nil || recordsBlobResp.SHA == "" {
		return prstate.Handle{}, fmt.Errorf("parsing records blob response: %w", err)
	}

	// 4. POST /git/trees naming both
	treePayload, err := json.Marshal(map[string]any{
		"tree": []map[string]string{
			{
				"path": "manifest.json",
				"mode": "100644",
				"type": "blob",
				"sha":  manifestBlobResp.SHA,
			},
			{
				"path": "records.json",
				"mode": "100644",
				"type": "blob",
				"sha":  recordsBlobResp.SHA,
			},
		},
	})
	if err != nil {
		return prstate.Handle{}, err
	}
	res = s.client.runInput(ctx, treePayload, "api", "--method", "POST",
		fmt.Sprintf("repos/%s/git/trees", ref.Repo.String()), "--input", "-")
	if !answered(res) {
		return prstate.Handle{}, publishFailure("creating tree", res)
	}
	var treeResp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(res.Stdout, &treeResp); err != nil || treeResp.SHA == "" {
		return prstate.Handle{}, fmt.Errorf("parsing tree response: %w", err)
	}

	// 5. POST /git/commits, parented on parent.Commit (or no parent if parent is zero)
	commitMsg := fmt.Sprintf("coverage: gen %d for pr %d slot %s at %s..%s",
		candCopy.Gen, ref.Number, ref.Slot,
		candCopy.Revision.Base.SHA(), candCopy.Revision.Head.SHA())

	parents := []string{}
	if parent.Commit != "" {
		parents = append(parents, parent.Commit)
	}
	commitPayload, err := json.Marshal(map[string]any{
		"message": commitMsg,
		"tree":    treeResp.SHA,
		"parents": parents,
	})
	if err != nil {
		return prstate.Handle{}, err
	}
	res = s.client.runInput(ctx, commitPayload, "api", "--method", "POST",
		fmt.Sprintf("repos/%s/git/commits", ref.Repo.String()), "--input", "-")
	if !answered(res) {
		return prstate.Handle{}, publishFailure("creating commit", res)
	}
	var commitResp struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(res.Stdout, &commitResp); err != nil || commitResp.SHA == "" {
		return prstate.Handle{}, fmt.Errorf("parsing commit response: %w", err)
	}

	// 6. POST or PATCH /git/refs. Force is required: a concurrent move or
	// a re-rooted chain makes the update a non-fast-forward, on any
	// namespace — and it still only moves this slot's ledger ref.
	postRes := s.client.run(ctx, "api", "--method", "POST",
		fmt.Sprintf("repos/%s/git/refs", ref.Repo.String()),
		"-f", "ref="+refName,
		"-f", "sha="+commitResp.SHA,
	)
	if !answered(postRes) {
		trimmed := strings.TrimPrefix(refName, "refs/")
		// A JSON body, not -f fields: force is a boolean and `-f
		// force=true` sends the string "true", which GitHub 422s.
		patchPayload, err := json.Marshal(map[string]any{
			"sha":   commitResp.SHA,
			"force": true,
		})
		if err != nil {
			return prstate.Handle{}, err
		}
		patchRes := s.client.runInput(ctx, patchPayload, "api", "--method", "PATCH",
			fmt.Sprintf("repos/%s/git/refs/%s", ref.Repo.String(), trimmed),
			"--input", "-",
		)
		if !answered(patchRes) {
			return prstate.Handle{}, publishFailure("updating ref "+refName, patchRes)
		}
	}

	return prstate.Handle{
		Gen:      candCopy.Gen,
		Commit:   commitResp.SHA,
		Location: refName,
		Degraded: candCopy.Form == prstate.GenerationCompact,
	}, nil
}

func (s *refStore) ReadGeneration(ctx context.Context, ref prstate.SlotRef, handle prstate.Handle) (prstate.Generation, error) {
	if err := ctx.Err(); err != nil {
		return prstate.Generation{}, err
	}

	if err := handle.Valid(); err != nil {
		return prstate.Generation{}, fmt.Errorf("%w: %v", prstate.ErrLedgerCorrupt, err)
	}
	if handle.Location == prstate.HandleMarker {
		return prstate.Generation{}, fmt.Errorf("%w: cannot read marker handle with ref store", prstate.ErrLedgerCorrupt)
	}
	if handle.Commit == "" {
		return prstate.Generation{}, prstate.ErrLedgerLost
	}

	// Read commit named in handle.Commit (never resolve the ref for authority!)
	res := s.client.run(ctx, "api", fmt.Sprintf("repos/%s/git/commits/%s", ref.Repo.String(), handle.Commit))
	if !answered(res) {
		if isNotFound(res) {
			return prstate.Generation{}, prstate.ErrLedgerLost
		}
		return prstate.Generation{}, failure("reading commit", res)
	}
	var commitObj struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(res.Stdout, &commitObj); err != nil {
		return prstate.Generation{}, fmt.Errorf("%w: malformed commit: %v", prstate.ErrLedgerCorrupt, err)
	}
	if commitObj.Tree.SHA == "" {
		return prstate.Generation{}, fmt.Errorf("%w: commit has no tree", prstate.ErrLedgerCorrupt)
	}

	// Read tree
	res = s.client.run(ctx, "api", fmt.Sprintf("repos/%s/git/trees/%s", ref.Repo.String(), commitObj.Tree.SHA))
	if !answered(res) {
		if isNotFound(res) {
			return prstate.Generation{}, prstate.ErrLedgerLost
		}
		return prstate.Generation{}, failure("reading tree", res)
	}
	var treeObj struct {
		SHA  string `json:"sha"`
		Tree []struct {
			Path string `json:"path"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(res.Stdout, &treeObj); err != nil {
		return prstate.Generation{}, fmt.Errorf("%w: malformed tree: %v", prstate.ErrLedgerCorrupt, err)
	}
	var manifestSHA, recordsSHA string
	for _, entry := range treeObj.Tree {
		switch entry.Path {
		case "manifest.json":
			manifestSHA = entry.SHA
		case "records.json":
			recordsSHA = entry.SHA
		}
	}
	if manifestSHA == "" || recordsSHA == "" {
		return prstate.Generation{}, fmt.Errorf("%w: tree missing manifest.json or records.json", prstate.ErrLedgerCorrupt)
	}

	// Read manifest blob
	res = s.client.run(ctx, "api", fmt.Sprintf("repos/%s/git/blobs/%s", ref.Repo.String(), manifestSHA))
	if !answered(res) {
		if isNotFound(res) {
			return prstate.Generation{}, prstate.ErrLedgerLost
		}
		return prstate.Generation{}, failure("reading manifest blob", res)
	}
	manifestBytes, err := parseBlobContent(res.Stdout)
	if err != nil {
		return prstate.Generation{}, fmt.Errorf("%w: reading manifest blob: %v", prstate.ErrLedgerCorrupt, err)
	}

	// Read records blob
	res = s.client.run(ctx, "api", fmt.Sprintf("repos/%s/git/blobs/%s", ref.Repo.String(), recordsSHA))
	if !answered(res) {
		if isNotFound(res) {
			return prstate.Generation{}, prstate.ErrLedgerLost
		}
		return prstate.Generation{}, failure("reading records blob", res)
	}
	recordsBytes, err := parseBlobContent(res.Stdout)
	if err != nil {
		return prstate.Generation{}, fmt.Errorf("%w: reading records blob: %v", prstate.ErrLedgerCorrupt, err)
	}

	// Decode and verify generation v2
	gen, err := prstate.DecodeGenerationV2(manifestBytes, recordsBytes)
	if err != nil {
		return prstate.Generation{}, fmt.Errorf("%w: %v", prstate.ErrLedgerCorrupt, err)
	}

	// A deleted ref is not a lost ledger: re-create it at the generation
	// just read, so the chain stays reachable. Create-only — when the ref
	// already exists (the ordinary read, or another writer racing this
	// one) there is nothing to restore, and a read must never move a ref
	// a newer publication owns. Best-effort: a denied or failed restore
	// does not fail a generation that already read.
	if refName, err := prstate.RefName(s.namespace, ref); err == nil {
		_ = s.client.run(ctx, "api", "--method", "POST",
			fmt.Sprintf("repos/%s/git/refs", ref.Repo.String()),
			"-f", "ref="+refName,
			"-f", "sha="+handle.Commit,
		)
	}

	return gen, nil
}

func parseBlobContent(raw []byte) ([]byte, error) {
	var blob struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(raw, &blob); err != nil {
		return nil, err
	}
	if blob.Encoding == "base64" {
		cleaned := strings.ReplaceAll(blob.Content, "\n", "")
		return base64.StdEncoding.DecodeString(cleaned)
	}
	return []byte(blob.Content), nil
}

func isNotFound(res exec.Result) bool {
	if res.Err != nil {
		return false
	}
	combined := string(res.Stderr) + string(res.Stdout)
	return strings.Contains(combined, "404") || strings.Contains(combined, "Not Found")
}
