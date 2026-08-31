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
	"github.com/carlosboeing/crossrev/internal/prstate"
)

type persistItem struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (l *Leg) persistDeferred(ctx context.Context, s *session, workdir string, recs []map[string]json.RawMessage, findings []map[string]json.RawMessage, sha string) (filed, matched int, wrote bool, lines string, out []map[string]json.RawMessage) {
	out = recs
	var b strings.Builder
	for i, d := range recs {
		if jsonString(d["resolution"]) != string(core.ResolutionDeferred) {
			continue
		}
		id := jsonString(d["finding_id"])
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
		dup, hasDup := issueNumber(d["duplicate_of"])
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
			landed, ok := l.persistOne(ctx, s, workdir, d, id)
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
			raw, _ := json.Marshal(tracked)
			d["crossrev_tracked"] = raw
			out[i] = d
		}
	}
	return filed, matched, wrote, b.String(), out
}

func (l *Leg) persistOne(ctx context.Context, s *session, workdir string, d map[string]json.RawMessage, id string) (string, bool) {
	if len(d["persist"]) == 0 || string(d["persist"]) == "null" {
		return "", false
	}
	var item persistItem
	if err := json.Unmarshal(d["persist"], &item); err != nil {
		return "", false
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
			return "", false
		}
		return s.repo.String() + "#" + strconv.Itoa(n), true
	case config.DestinationRepository:
		dir := s.backlog.Path
		if err := config.AssertPathInsideRepo(workdir, dir); err != nil {
			return "", false
		}
		if s.backlog.Layout == config.LayoutFile {
			full := filepath.Join(workdir, dir)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return "", false
			}
			f, err := os.OpenFile(full, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return "", false
			}
			_, err = fmt.Fprintf(f, "\n## %s\n\n%s\n", item.Title, body)
			_ = f.Close()
			if err != nil {
				return "", false
			}
			return dir, true
		}
		full := filepath.Join(workdir, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			return "", false
		}
		target := filepath.Join(dir, id+".md")
		if err := os.WriteFile(filepath.Join(workdir, target), []byte(fmt.Sprintf("# %s\n\n%s\n", item.Title, body)), 0o644); err != nil {
			return "", false
		}
		return target, true
	default:
		return "", false
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

func issueNumber(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var n int
	if json.Unmarshal(raw, &n) == nil && n != 0 {
		return n, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" && s != "null" {
		n, err := strconv.Atoi(s)
		return n, err == nil && n != 0
	}
	return 0, false
}

func marshalResolutions(recs []map[string]json.RawMessage) json.RawMessage {
	b, err := json.Marshal(recs)
	if err != nil {
		return json.RawMessage("[]")
	}
	return b
}

func unmarshalResolutions(raw json.RawMessage) []map[string]json.RawMessage {
	var recs []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &recs); err != nil || recs == nil {
		return []map[string]json.RawMessage{}
	}
	return recs
}

func unmarshalFindings(raw json.RawMessage) []map[string]json.RawMessage {
	var fs []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fs); err != nil || fs == nil {
		return []map[string]json.RawMessage{}
	}
	return fs
}
