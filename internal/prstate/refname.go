package prstate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var slotRegex = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidNamespace refuses anything that could put CrossRev outside its own
// corner of the ref space. CrossRev never creates a branch or a tag; this
// is where that promise is kept rather than documented.
//
// Refused: anything not under refs/; refs/heads, refs/tags, refs/pull and
// refs/remotes and their children; a component that is empty, starts with a
// dot, ends in .lock, or contains "..", "@{", ASCII control characters,
// space, or any of ~ ^ : ? * [ \.
func ValidNamespace(ns string) error {
	if ns == "" {
		return errors.New("empty namespace")
	}
	if !strings.HasPrefix(ns, "refs/") {
		return fmt.Errorf("namespace %q must be under refs/", ns)
	}
	parts := strings.Split(ns, "/")
	if len(parts) < 2 {
		return fmt.Errorf("namespace %q has no component under refs/", ns)
	}
	second := parts[1]
	if second == "heads" || second == "tags" || second == "pull" || second == "remotes" {
		return fmt.Errorf("namespace %q is under reserved refs/%s", ns, second)
	}
	for i, part := range parts[1:] {
		if part == "" {
			return fmt.Errorf("namespace %q has empty component at %d", ns, i+1)
		}
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("namespace component %q starts with a dot", part)
		}
		if strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("namespace component %q ends in .lock", part)
		}
		if strings.Contains(part, "..") {
			return fmt.Errorf("namespace component %q contains ..", part)
		}
		if strings.Contains(part, "@{") {
			return fmt.Errorf("namespace component %q contains @{", part)
		}
		for _, r := range part {
			if r <= 0x1f || r == 0x7f {
				return fmt.Errorf("namespace component %q contains ASCII control character 0x%02x", part, r)
			}
			if r == ' ' {
				return fmt.Errorf("namespace component %q contains space", part)
			}
			if strings.ContainsRune("~^:?*[\\]", r) {
				return fmt.Errorf("namespace component %q contains forbidden character %c", part, r)
			}
		}
	}
	return nil
}

// ValidSlot refuses a slot id that is not one path component of safe
// characters: [A-Za-z0-9][A-Za-z0-9._-]{0,63}.
func ValidSlot(slot string) error {
	if !slotRegex.MatchString(slot) {
		return fmt.Errorf("slot %q is not a valid slot name", slot)
	}
	return nil
}

// RefName is <namespace>/pr/<number>/<slot>/coverage. It revalidates both
// inputs, because a value can reach a store from a test or a later caller
// that never passed through config validation, and the one write this guards
// is irreversible in someone else's repository.
func RefName(namespace string, ref SlotRef) (string, error) {
	if err := ValidNamespace(namespace); err != nil {
		return "", err
	}
	slot := ref.Slot
	if slot == "" {
		slot = DefaultSlot
	}
	if err := ValidSlot(slot); err != nil {
		return "", err
	}
	if ref.Number <= 0 {
		return "", fmt.Errorf("invalid PR number: %d", ref.Number)
	}
	return fmt.Sprintf("%s/pr/%d/%s/coverage", namespace, ref.Number, slot), nil
}
