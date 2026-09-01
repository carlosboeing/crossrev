package resolve

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/carlosboeing/crossrev/internal/harness"
)

// Run selects the current review pass, claims once, invokes a write-capable
// resolver, then replies, persists, commits and pushes.
func (l *Leg) Run(ctx context.Context, req Request) Result {
	if req.PR == 0 {
		return refuse("crossrev resolve needs a pull request number", "Usage: crossrev resolve --pr 42")
	}
	if req.Trigger == "" {
		req.Trigger = TriggerHuman
	}
	if req.Trigger != TriggerHuman && req.Trigger != TriggerAutomatic {
		return refuse(fmt.Sprintf("unknown resolve trigger: %s", req.Trigger),
			"Use --trigger human or --trigger automatic.")
	}
	s, early := l.load(ctx, req)
	if early.Outcome != "" || early.Err != nil {
		if early.Outcome == OutcomeNoFindings || early.Outcome == OutcomeHalted {
			return l.finishEmpty(ctx, s, early)
		}
		return early
	}
	if l.Log != nil {
		l.Log.SetLeg("resolve")
		head := ""
		if !s.pr.HeadRefOid.IsZero() {
			head = s.pr.HeadRefOid.Short()
		}
		l.Log.Event("leg", fmt.Sprintf("resolve trigger=%s mode=%s head=%s", s.req.Trigger, s.mode, head))
	}

	if refusal, warn, err := l.settings(s); err != nil {
		return wrapErr(err)
	} else if refusal != nil {
		return Result{Outcome: OutcomeRefused, Err: refusal, Pass: s.pass}
	} else if warn != "" {
		early.Messages = append(early.Messages, warn)
	}

	workdir, err := l.prepareWorktree(ctx, s)
	if err != nil {
		r := wrapErr(err)
		r.Pass = s.pass
		return r
	}

	marker, claimWarning, err := l.claim(ctx, s)
	if err != nil {
		r := wrapErr(err)
		r.Pass = s.pass
		return r
	}
	if claimWarning != "" {
		early.Messages = append(early.Messages, claimWarning)
	}

	got := l.invoke(ctx, s, marker, workdir)
	got.Pass = s.pass
	if got.Marker.CommentID() == 0 {
		got.Marker = marker
	}
	got.Messages = append(early.Messages, got.Messages...)
	if got.Outcome != OutcomeInvoked || got.Err != nil {
		return got
	}
	published := l.publish(ctx, s, got, workdir)
	return published
}

func (l *Leg) document() (harness.Document, error) {
	if len(l.Harness.Names()) > 0 {
		return l.Harness, nil
	}
	return harness.Load(harness.DescriptorJSON())
}

func (l *Leg) adapterFor(doc harness.Document, name string) (harness.Adapter, error) {
	if l.Adapter != nil {
		return l.Adapter, nil
	}
	a, ok := harness.For(doc, name)
	if !ok {
		return nil, &Refusal{
			Message: fmt.Sprintf("there is no adapter for the harness '%s'", name),
			Hint:    "CrossRev drives claude, codex, agy, grok and opencode directly.",
		}
	}
	return a, nil
}

func harnessFor(doc harness.Document, name string) (harness.Adapter, bool) {
	return harness.For(doc, name)
}

func (l *Leg) binaryInstalled(name string) bool {
	doc, err := l.document()
	binary := name
	if err == nil {
		if entry, ok := doc.For(name); ok && entry.Binary != "" {
			binary = entry.Binary
		}
	}
	look := l.LookPath
	if look == nil {
		look = lookPath
	}
	_, err = look(binary)
	return err == nil
}

func lookPath(name string) (string, error) {
	if name == "" {
		return "", os.ErrNotExist
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		if _, err := os.Stat(name); err != nil {
			return "", err
		}
		return name, nil
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}
