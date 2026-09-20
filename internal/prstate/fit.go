package prstate

import "fmt"

const CommentCap = 65536

// FitMarkerComment is the one bound. body is the whole comment about to be
// written — prose, the encoded marker, and whatever generations it carries.
// It runs at every marker write, because publication is not the only thing
// that grows this comment.
func FitMarkerComment(body string) error {
	if len(body) > CommentCap {
		return fmt.Errorf("marker comment of %d bytes exceeds the %d-byte cap", len(body), CommentCap)
	}
	return nil
}
