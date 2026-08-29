package ghexec_test

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The GraphQL documents are transcribed from lib/github.sh, so they are
// compared against it rather than against a copy of themselves.
func TestGraphQLDocumentsMatchTheShell(t *testing.T) {
	src, err := os.ReadFile("../../../lib/github.sh")
	if err != nil {
		t.Fatalf("reading lib/github.sh: %v", err)
	}
	shell := string(src)

	for _, c := range []struct {
		name string
		got  string
	}{
		{"reviewThreads", threadsQueryFromCode(t)},
		{"resolveReviewThread", resolveMutationFromCode(t)},
	} {
		if !strings.Contains(shell, c.got) {
			t.Errorf("the %s document is not in lib/github.sh verbatim:\n%s", c.name, c.got)
		}
	}
}

func TestReviewThreadsArgv(t *testing.T) {
	// The two flags are set the opposite way round on the two threads, because
	// they drive different decisions and a fixture that sets both the same way
	// cannot tell them apart. An open thread on a line that has since changed
	// is outdated and unresolved; a resolved thread on a line still in the
	// diff is the reverse.
	body := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
	  {"id":"THREAD1","isResolved":false,"isOutdated":true,"path":"app.ts","line":40,
	   "comments":{"nodes":[
	     {"databaseId":5000,"author":{"login":"carlosboeing"},
	      "body":"finding <!-- crossrev:f {\"id\":\"aaaa000000000001\",\"pass\":1,\"leg\":\"review\"} -->"},
	     {"databaseId":5001,"author":{"login":"someone"},"body":"agreed"}]}},
	  {"id":"THREAD2","isResolved":true,"isOutdated":false,"path":"lib.ts","line":null,
	   "comments":{"nodes":[]}}]}}}}}`

	c, r := client(t, out(body))
	got := c.ReviewThreads(context.Background(), testSlug(t), 42)

	r.wantArgs(t, 0, "api", "graphql", "-F", "owner=acme", "-F", "name=widget", "-F", "number=42",
		"-f", "query="+threadsQueryFromCode(t))

	if len(got) != 2 {
		t.Fatalf("threads = %+v, want two", got)
	}
	first := got[0]
	if first.ID != "THREAD1" || first.Path != "app.ts" || first.Line != 40 {
		t.Errorf("thread = %+v", first)
	}
	if first.IsResolved || !first.IsOutdated {
		t.Errorf("thread = %+v, want open and outdated", first)
	}
	if first.RootCommentID != 5000 {
		t.Errorf("root comment = %d, want the first comment in the thread", first.RootCommentID)
	}
	if len(first.FindingIDs) != 1 || first.FindingIDs[0].String() != "aaaa000000000001" {
		t.Errorf("finding ids = %v", first.FindingIDs)
	}
	if len(first.Comments) != 2 || first.Comments[0].Author != "carlosboeing" || first.Comments[1].Body != "agreed" {
		t.Errorf("comments = %+v", first.Comments)
	}

	second := got[1]
	if !second.IsResolved || second.IsOutdated {
		t.Errorf("thread = %+v, want resolved and current", second)
	}
	if second.Line != 0 || second.RootCommentID != 0 {
		t.Errorf("a null line and an empty thread should read as zero: %+v", second)
	}
}

func TestReviewThreadsAnswersEmptyOnARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if got := c.ReviewThreads(context.Background(), testSlug(t), 42); len(got) != 0 {
		t.Errorf("threads = %+v, want none", got)
	}
}

// A marker that will not parse costs its own id and nothing else.
func TestReviewThreadsKeepsThreadsAroundAnUnreadableMarker(t *testing.T) {
	body := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
	  {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.ts","line":1,
	   "comments":{"nodes":[{"databaseId":1,"author":{"login":"a"},"body":"bad <!-- crossrev:f {oops} -->"}]}},
	  {"id":"T2","isResolved":false,"isOutdated":false,"path":"b.ts","line":2,
	   "comments":{"nodes":[{"databaseId":2,"author":{"login":"a"},
	     "body":"good <!-- crossrev:f {\"id\":\"aaaa000000000002\"} -->"}]}}]}}}}}`

	c, _ := client(t, out(body))
	got := c.ReviewThreads(context.Background(), testSlug(t), 42)

	if len(got) != 2 {
		t.Fatalf("threads = %+v, want both", got)
	}
	if len(got[0].FindingIDs) != 0 {
		t.Errorf("finding ids = %v, want none from an unreadable marker", got[0].FindingIDs)
	}
	if len(got[1].FindingIDs) != 1 {
		t.Errorf("finding ids = %v, want the readable one", got[1].FindingIDs)
	}
}

// A readable marker with no usable id yields nothing. jq carries a null into
// the array and carries a malformed id as written; neither could ever match an
// id the review leg minted.
func TestReviewThreadsDropsAMarkerWithNoUsableID(t *testing.T) {
	body := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
	  {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.ts","line":1,
	   "comments":{"nodes":[
	     {"databaseId":1,"author":{"login":"a"},"body":"no id <!-- crossrev:f {\"pass\":1} -->"},
	     {"databaseId":2,"author":{"login":"a"},"body":"short <!-- crossrev:f {\"id\":\"abc\"} -->"},
	     {"databaseId":3,"author":{"login":"a"},
	      "body":"good <!-- crossrev:f {\"id\":\"aaaa000000000003\"} -->"}]}}]}}}}}`

	c, _ := client(t, out(body))
	got := c.ReviewThreads(context.Background(), testSlug(t), 42)
	if len(got) != 1 {
		t.Fatalf("threads = %+v", got)
	}
	if len(got[0].FindingIDs) != 1 || got[0].FindingIDs[0].String() != "aaaa000000000003" {
		t.Errorf("finding ids = %v, want the minted one alone", got[0].FindingIDs)
	}
}

// Two markers in one comment are two ids.
//
// The payload class is `[^}]*` rather than `.*` on purpose, and this is the
// body that says why: a greedy match runs from the first opening brace to the
// last closing one, reads the pair plus the text between them as a single
// payload, and loses both ids to one JSON failure.
func TestReviewThreadsReadsTwoMarkersInOneComment(t *testing.T) {
	body := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
	  {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.ts","line":1,
	   "comments":{"nodes":[{"databaseId":1,"author":{"login":"a"},
	     "body":"first <!-- crossrev:f {\"id\":\"aaaa000000000001\"} --> and second <!-- crossrev:f {\"id\":\"aaaa000000000002\"} -->"}]}}]}}}}}`

	c, _ := client(t, out(body))
	got := c.ReviewThreads(context.Background(), testSlug(t), 42)
	if len(got) != 1 {
		t.Fatalf("threads = %+v", got)
	}
	ids := got[0].FindingIDs
	if len(ids) != 2 {
		t.Fatalf("finding ids = %v, want both", ids)
	}
	if ids[0].String() != "aaaa000000000001" || ids[1].String() != "aaaa000000000002" {
		t.Errorf("finding ids = %v, want them in the order they appear", ids)
	}
}

// An author GitHub reports as null is a deleted account, not a parse failure.
func TestReviewThreadsReadsACommentWithNoAuthor(t *testing.T) {
	body := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
	  {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.ts","line":1,
	   "comments":{"nodes":[{"databaseId":1,"author":null,"body":"orphaned"}]}}]}}}}}`

	c, _ := client(t, out(body))
	got := c.ReviewThreads(context.Background(), testSlug(t), 42)
	if len(got) != 1 || len(got[0].Comments) != 1 {
		t.Fatalf("threads = %+v", got)
	}
	if got[0].Comments[0].Author != "" {
		t.Errorf("author = %q, want empty", got[0].Comments[0].Author)
	}
}

func TestThreadResolveArgv(t *testing.T) {
	c, r := client(t)
	if err := c.ThreadResolve(context.Background(), "THREAD1"); err != nil {
		t.Fatalf("ThreadResolve: %v", err)
	}
	r.wantArgs(t, 0, "api", "graphql", "-f", "threadId=THREAD1", "-f", "query="+resolveMutationFromCode(t))
}

func TestThreadResolveReportsARefusal(t *testing.T) {
	c, _ := client(t, bad())
	if err := c.ThreadResolve(context.Background(), "THREAD1"); err == nil {
		t.Error("a refused resolution reported success")
	}
}

// The documents under test, read back off the argv the client builds, so the
// comparison against lib/github.sh is against what actually gets sent.
func threadsQueryFromCode(t *testing.T) string {
	t.Helper()
	c, r := client(t, out("{}"))
	c.ReviewThreads(context.Background(), testSlug(t), 42)
	return strings.TrimPrefix(r.specs[0].Args[9], "query=")
}

func resolveMutationFromCode(t *testing.T) string {
	t.Helper()
	c, r := client(t)
	_ = c.ThreadResolve(context.Background(), "T")
	return strings.TrimPrefix(r.specs[0].Args[5], "query=")
}
