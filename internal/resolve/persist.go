package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/carlosboeing/crossrev/internal/config"
	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/harness"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

type persistItem struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (l *Leg) persistDeferred(ctx context.Context, s *session, workdir string, recs []harness.Node, findings []harness.Node, sha string) (filed, matched int, wrote bool, lines string, out []harness.Node, messages []string) {
	out = make([]harness.Node, len(recs))
	copy(out, recs)
	var b strings.Builder
	for i, d := range recs {
		if d.Member("resolution").StringVal() != string(core.ResolutionDeferred) {
			continue
		}
		id := d.Member("finding_id").StringVal()
		f := findingByID(findings, id)
		where := findingLocation(f, sha, s.repo, s.req.PR)

		tracked := ""
		existing := 0
		if s.backlog.Destination == config.DestinationGitHubIssues {
			fid, err := prstate.ParseFindingID(id)
			if err == nil {
				n, ok := l.Forge.IssueByFinding(ctx, s.repo, s.trackingLabel(), fid)
				if ok {
					existing = n
				}
			}
		}
		dup, hasDup := issueNumber(d.Member("duplicate_of"))
		switch {
		case existing > 0:
			tracked = s.repo.String() + "#" + strconv.Itoa(existing)
			matched++
			fmt.Fprintf(&b, "\n- %s — already tracked as #%d, so nothing was filed", where, existing)
		case hasDup:
			tracked = s.repo.String() + "#" + strconv.Itoa(dup)
			matched++
			fmt.Fprintf(&b, "\n- %s — matches the existing issue #%d, so nothing was filed", where, dup)
			if s.cfg.Get(".backlog.github_issues.comment_on_existing_issue") == "true" {
				marker := ""
				if fid, err := prstate.ParseFindingID(id); err == nil {
					marker = prstate.EncodeFindingMarker(fid, s.pass, core.LegResolve)
				}
				l.Forge.IssueCommentCreate(ctx, s.repo, dup,
					fmt.Sprintf("Seen again while reviewing %s#%d (crossrev pass %d).%s", s.repo, s.req.PR, s.pass, marker))
			}
		default:
			landed, ok, persistWarning := l.persistOne(ctx, s, workdir, d, id)
			if persistWarning != "" {
				messages = append(messages, persistWarning)
			}
			if ok {
				tracked = landed
				filed++
				if s.backlog.Destination == config.DestinationRepository {
					wrote = true
				}
				shown := strings.TrimPrefix(tracked, s.repo.String())
				fmt.Fprintf(&b, "\n- %s — filed as %s", where, shown)
			} else {
				fmt.Fprintf(&b, "\n- %s — **not persisted anywhere**, so its thread stays open rather than resolving against a write that did not land", where)
			}
		}
		if s.backlog.Destination != config.DestinationNone {
			d.Set("crossrev_tracked", harness.FromString(tracked))
			out[i] = d
		}
	}
	return filed, matched, wrote, b.String(), out, messages
}

func (l *Leg) persistOne(ctx context.Context, s *session, workdir string, d harness.Node, id string) (string, bool, string) {
	persistNode := d.Member("persist")
	if !persistNode.Present() || persistNode.IsNull() {
		return "", false, ""
	}
	raw, err := json.Marshal(persistNode)
	if err != nil {
		return "", false, ""
	}
	var item persistItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return "", false, ""
	}

	footer := fmt.Sprintf("\n\n---\nFound by crossrev while reviewing %s#%d (pass %d). Verified against the codebase before filing: one model raised it, a second confirmed it is real, and it was left out of that pull request deliberately rather than missed.",
		s.repo, s.req.PR, s.pass)
	if fid, err := prstate.ParseFindingID(id); err == nil {
		footer += prstate.EncodeFindingMarker(fid, s.pass, core.LegResolve)
	}
	body := item.Body + footer

	switch s.backlog.Destination {
	case config.DestinationGitHubIssues:
		labels := []string{s.trackingLabel()}
		labels = append(labels, s.backlogLabels()...)
		n, err := l.Forge.IssueCreate(ctx, s.repo, item.Title, body, labels)
		if err != nil || n == 0 {
			return "", false, warning(
				"could not file an issue on "+s.repo.String()+" for a deferred finding",
				"The thread stays open and unresolved instead, so the finding is still visible on the pull request. Check that the backlog labels exist and the token has issues write.",
			)
		}
		return s.repo.String() + "#" + strconv.Itoa(n), true, ""
	case config.DestinationRepository:
		dir := s.backlog.Path
		if err := config.AssertPathInsideRepo(workdir, dir); err != nil {
			return "", false, ""
		}
		if s.backlog.Layout == config.LayoutFile {
			full := filepath.Join(workdir, dir)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return "", false, ""
			}
			f, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return "", false, ""
			}
			_, err = fmt.Fprintf(f, "\n## %s\n\n%s\n", item.Title, body)
			_ = f.Close()
			if err != nil {
				return "", false, ""
			}
			return dir, true, ""
		}
		full := filepath.Join(workdir, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return "", false, ""
		}
		target := filepath.Join(dir, id+".md")
		if err := os.WriteFile(filepath.Join(workdir, target), []byte(fmt.Sprintf("# %s\n\n%s\n", item.Title, body)), 0o644); err != nil {
			return "", false, ""
		}
		return target, true, ""
	default:
		return "", false, ""
	}
}

func (s *session) trackingLabel() string {
	label := s.cfg.Get(".backlog.github_issues.tracking_label")
	if label == "" {
		return "crossrev-review"
	}
	return label
}

func (s *session) backlogLabels() []string {
	raw := s.cfg.GetJSON(".backlog.github_issues.labels")
	var labels []string
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil
	}
	return labels
}

func (s *session) gitHooksRun() bool {
	return s.cfg.Get(".git.hooks") == "run"
}

func issueNumber(node harness.Node) (int, bool) {
	if !node.Present() || node.IsNull() {
		return 0, false
	}
	if n, ok := node.AsInt(); ok && n != 0 {
		return int(n), true
	}
	if s, ok := node.AsString(); ok && s != "" && s != "null" {
		n, err := strconv.Atoi(s)
		return n, err == nil && n != 0
	}
	return 0, false
}

func marshalResolutions(recs []harness.Node) json.RawMessage {
	b, err := json.Marshal(recs)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}

func unmarshalResolutions(raw json.RawMessage) []harness.Node {
	var recs []harness.Node
	if err := json.Unmarshal(raw, &recs); err != nil || recs == nil {
		return []harness.Node{}
	}
	return recs
}

func unmarshalFindings(raw json.RawMessage) []harness.Node {
	var fs []harness.Node
	if err := json.Unmarshal(raw, &fs); err != nil || fs == nil {
		return []harness.Node{}
	}
	return fs
}
