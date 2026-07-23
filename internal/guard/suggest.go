package guard

import "sort"

const (
	// suggestMaxDist is the largest edit distance we will treat as a "did you
	// mean" match. 2 catches the common hallucination shapes — a
	// transposed/missing pair of characters or a case slip (getUser vs GetUser is
	// distance 1) — without the noise that a looser threshold produces on short
	// identifiers.
	suggestMaxDist = 2

	// suggestMaxCandidates caps how many names are offered. More than a few stops
	// being a hint and becomes a list to read: the point is to turn a stop into a
	// fix, and an agent handed eight near-identical names is no better off than
	// one handed none.
	suggestMaxCandidates = 3
)

// withinFloor rejects a match whose distance is large RELATIVE to the name it is
// matching. A flat distance-2 threshold is too loose on short identifiers: two
// edits on a three-character name have changed most of it, so `abc` would draw a
// confident suggestion of `xyc` — a different name, not a typo of this one.
//
// 2*dist <= len means: names of 1-3 characters accept only a single edit, names
// of 4+ accept the full suggestMaxDist. It therefore only bites below four
// characters, which is exactly where a two-edit "match" stops being evidence of
// anything.
func withinFloor(dist, nameLen int) bool { return 2*dist <= nameLen }

// Suggest returns up to suggestMaxCandidates known symbols closest to name by
// Levenshtein distance, nearest first, when they are within suggestMaxDist and
// pass withinFloor. It is the model-free "did you mean" behind a guard
// violation: a hallucinated reference is usually a near-miss of a symbol that
// really exists.
//
// Deterministic: candidates sort by (distance, name), so equal-distance ties
// resolve lexicographically and the same input always yields the same list
// regardless of map iteration order.
func Suggest(name string, known map[string]struct{}) ([]string, bool) {
	type scored struct {
		name string
		dist int
	}
	var hits []scored

	// One scratch buffer reused across every candidate. Allocating inside the
	// distance function instead cost ~18k allocations and ~2.8 MB per call on a
	// realistic symbol set — a third of the hook's entire 12 ms budget, spent
	// entirely on garbage. See levenshteinWith.
	var rows levRows

	for cand := range known {
		// Cheap length prune: distance is at least the length difference. This
		// helps far less than it looks — real codebases cluster name lengths, so
		// a large fraction of candidates survive it and reach the distance
		// computation. It is a filter, not the thing that makes this cheap.
		d := len(cand) - len(name)
		if d < 0 {
			d = -d
		}
		if d > suggestMaxDist {
			continue
		}
		dist := levenshteinWith(name, cand, suggestMaxDist, &rows)
		if dist > suggestMaxDist || !withinFloor(dist, len(name)) {
			continue
		}
		hits = append(hits, scored{cand, dist})
	}
	if len(hits) == 0 {
		return nil, false
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].dist != hits[j].dist {
			return hits[i].dist < hits[j].dist
		}
		return hits[i].name < hits[j].name
	})
	if len(hits) > suggestMaxCandidates {
		hits = hits[:suggestMaxCandidates]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out, true
}

// levRows is the reusable row pair for the distance computation. Reused across
// every candidate in one Suggest call so the cost is two allocations per call
// rather than one per character per candidate.
type levRows struct {
	prev, curr []int
}

// ensure sizes both rows to n, growing the backing arrays only when a longer
// candidate than any seen so far arrives.
func (r *levRows) ensure(n int) {
	if cap(r.prev) < n {
		r.prev = make([]int, n)
		r.curr = make([]int, n)
	}
	r.prev, r.curr = r.prev[:n], r.curr[:n]
}

// levenshtein computes the edit distance between a and b, short-circuiting once
// the running minimum of a row exceeds max. Convenience wrapper that owns its
// scratch; hot paths should use levenshteinWith and reuse one.
func levenshtein(a, b string, max int) int {
	var rows levRows
	return levenshteinWith(a, b, max, &rows)
}

// levenshteinWith is levenshtein over a caller-owned scratch buffer.
//
// The rows are swapped rather than reallocated each iteration. The previous
// version allocated a fresh row per character of a, which meant a 17-character
// name compared against a thousand length-matched candidates allocated ~18,000
// slices for a single suggestion — invisible in a unit test, and a third of the
// PreToolUse budget in production.
func levenshteinWith(a, b string, max int, rows *levRows) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	rows.ensure(lb + 1)
	prev, curr := rows.prev, rows.curr
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
			if curr[j] < rowMin {
				rowMin = curr[j]
			}
		}
		if rowMin > max {
			return max + 1
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
