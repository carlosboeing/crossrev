package ghexec

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"github.com/carlosboeing/crossrev/internal/core"
	"github.com/carlosboeing/crossrev/internal/forge"
	"github.com/carlosboeing/crossrev/internal/prstate"
)

// threadsQuery is the read at lib/github.sh:120-131, transcribed byte for byte.
//
// Thread identity comes from GraphQL because the REST review-comment API does
// not expose the node id the resolve mutation needs. A test compares this
// against lib/github.sh, so the two cannot drift.
const threadsQuery = `
    query($owner:String!,$name:String!,$number:Int!) {
      repository(owner:$owner,name:$name) {
        pullRequest(number:$number) {
          reviewThreads(first:100) {
            nodes {
              id isResolved isOutdated path line
              comments(first:30) { nodes { databaseId body author { login } } }
            }
          }
        }
      }
    }`

// resolveMutation is the write at lib/github.sh:243-245.
const resolveMutation = `
    mutation($threadId:ID!) {
      resolveReviewThread(input:{threadId:$threadId}) { thread { isResolved } }
    }`

// findingMarker is the scan lib/github.sh:130 and :334 both apply: the payload
// of a finding marker, with no closing brace inside it.
var findingMarker = regexp.MustCompile(`<!-- crossrev:f (\{[^}]*\}) -->`)

// ReviewThreads is every inline conversation, with the finding ids its comments
// carry.
func (c *Client) ReviewThreads(ctx context.Context, repo core.Slug, number int) []forge.ReviewThread {
	res := c.run(ctx, "api", "graphql",
		"-F", "owner="+repo.Owner(),
		"-F", "name="+repo.Name(),
		"-F", "number="+strconv.Itoa(number),
		"-f", "query="+threadsQuery)
	if !answered(res) {
		return nil
	}

	var body struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							ID         string `json:"id"`
							IsResolved bool   `json:"isResolved"`
							IsOutdated bool   `json:"isOutdated"`
							Path       string `json:"path"`
							Line       *int   `json:"line"`
							Comments   struct {
								Nodes []struct {
									DatabaseID *int64 `json:"databaseId"`
									Body       string `json:"body"`
									Author     *struct {
										Login string `json:"login"`
									} `json:"author"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Stdout, &body); err != nil {
		// The shell's `|| printf '[]'` covers this too: jq fails on an answer
		// it cannot read and the caller sees no threads.
		return nil
	}

	nodes := body.Data.Repository.PullRequest.ReviewThreads.Nodes
	threads := make([]forge.ReviewThread, 0, len(nodes))
	for _, n := range nodes {
		thread := forge.ReviewThread{
			ID:         n.ID,
			IsResolved: n.IsResolved,
			IsOutdated: n.IsOutdated,
			Path:       n.Path,
		}
		if n.Line != nil {
			thread.Line = *n.Line
		}
		if len(n.Comments.Nodes) > 0 && n.Comments.Nodes[0].DatabaseID != nil {
			thread.RootCommentID = *n.Comments.Nodes[0].DatabaseID
		}
		for _, comment := range n.Comments.Nodes {
			author := ""
			if comment.Author != nil {
				author = comment.Author.Login
			}
			thread.Comments = append(thread.Comments, forge.ThreadComment{Author: author, Body: comment.Body})
			for _, id := range findingIDsIn(comment.Body) {
				// Read through prstate, which is the only place an id is
				// minted. An id no hash could have produced is dropped rather
				// than carried, and dropping one cannot lose a match: every id
				// it is compared against was minted the same way.
				parsed, err := prstate.ParseFindingID(id)
				if err != nil {
					continue
				}
				thread.FindingIDs = append(thread.FindingIDs, parsed)
			}
		}
		threads = append(threads, thread)
	}
	if len(threads) == 0 {
		return nil
	}
	return threads
}

// findingIDsIn reads the finding ids out of one comment body, in the order they
// appear.
//
// One divergence from the shell, and it is the one internal/prstate already
// took at comments.go:100-123 for the same reason. jq aborts the whole program
// on the first payload `fromjson` refuses, so a single unreadable marker
// anywhere in a pull request costs the shell every thread on it — the
// `|| printf '[]'` catches the failure and the caller reads no threads at all.
// Here the unreadable marker costs its own id and nothing else. Go finding ids
// the shell misses is the safe direction: an id already in the set is one the
// leg does not act on twice.
func findingIDsIn(body string) []string {
	matches := findingMarker.FindAllStringSubmatch(body, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		var payload struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(m[1]), &payload); err != nil {
			continue
		}
		if payload.ID != "" {
			ids = append(ids, payload.ID)
		}
	}
	return ids
}

// ThreadResolve marks a review thread resolved.
func (c *Client) ThreadResolve(ctx context.Context, threadID string) error {
	res := c.run(ctx, "api", "graphql", "-f", "threadId="+threadID, "-f", "query="+resolveMutation)
	if !answered(res) {
		return failure(fmt.Sprintf("could not resolve review thread %s", threadID), res)
	}
	return nil
}
