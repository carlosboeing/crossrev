package vcs

// The multi-pattern matcher behind the advisory blob pass: Aho-Corasick over
// the whole term set (Aho & Corasick, "Efficient String Matching", CACM
// 1975), so one walk over each blob answers every term with its match byte
// offsets. Terms are literal bytes — identifiers, changed paths, leading
// dashes — and matching is substring, the way the git grep -F pass it
// replaces matched.

type acNode struct {
	next map[byte]int
	fail int
	out  []int
}

// matcher answers which terms hold where in one left-to-right walk. Terms
// must be non-empty; an empty pattern matches everywhere, so the caller
// drops empties before building.
type matcher struct {
	nodes []acNode
	// root answers the first byte without a map lookup: most bytes miss
	// every term at the root, so the common step is one array index.
	// Zero means the root itself.
	root [256]int
}

// newMatcher builds the automaton for terms. Duplicate terms share one
// output index each — the caller dedupes, so every index names one term.
func newMatcher(terms []string) *matcher {
	m := &matcher{nodes: []acNode{{}}}
	for idx, term := range terms {
		state := 0
		for i := 0; i < len(term); i++ {
			c := term[i]
			next, ok := m.nodes[state].next[c]
			if !ok {
				next = len(m.nodes)
				if m.nodes[state].next == nil {
					m.nodes[state].next = make(map[byte]int)
				}
				m.nodes[state].next[c] = next
				m.nodes = append(m.nodes, acNode{})
			}
			state = next
		}
		m.nodes[state].out = append(m.nodes[state].out, idx)
	}
	// Failure links, breadth-first: a missing transition from the root loops
	// to the root, and every other missing transition follows the failure
	// chain at scan time. Each state inherits its failure target's outputs,
	// so the scan emits matches without walking the chain per byte.
	var queue []int
	for c, next := range m.nodes[0].next {
		m.nodes[next].fail = 0
		m.root[c] = next
		queue = append(queue, next)
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for c, next := range m.nodes[state].next {
			fail := m.nodes[state].fail
			for fail != 0 {
				if _, ok := m.nodes[fail].next[c]; ok {
					break
				}
				fail = m.nodes[fail].fail
			}
			if target, ok := m.nodes[fail].next[c]; ok {
				fail = target
			}
			m.nodes[next].fail = fail
			m.nodes[next].out = append(m.nodes[next].out, m.nodes[fail].out...)
			queue = append(queue, next)
		}
	}
	return m
}

// scan walks data once, calling emit for every term ending at each byte
// offset: the term's index and the offset of its last byte. Matches overlap
// — every pattern ending at an offset reports, not just the longest.
func (m *matcher) scan(data []byte, emit func(term int, end int)) {
	state := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if state == 0 {
			state = m.root[c]
		} else {
			for {
				if next, ok := m.nodes[state].next[c]; ok {
					state = next
					break
				}
				state = m.nodes[state].fail
				if state == 0 {
					state = m.root[c]
					break
				}
			}
		}
		for _, term := range m.nodes[state].out {
			emit(term, i)
		}
	}
}
