package guard

// suggestMaxDist is the largest edit distance we will treat as a "did you mean"
// match. 2 catches the common hallucination shapes — a transposed/missing pair
// of characters or a case slip (getUser vs GetUser is distance 1) — without the
// noise that a looser threshold produces on short identifiers.
const suggestMaxDist = 2

// Suggest returns the known symbol closest to name by Levenshtein distance, when
// that distance is within suggestMaxDist. It is the model-free "did you mean"
// behind a guard violation: a hallucinated reference is usually a near-miss of a
// symbol that really exists. Deterministic — ties resolve to the
// lexicographically smallest candidate so the same input always suggests the same
// name.
func Suggest(name string, known map[string]struct{}) (string, bool) {
	best := ""
	bestDist := suggestMaxDist + 1
	for cand := range known {
		// Cheap length prune: distance is at least the length difference.
		d := len(cand) - len(name)
		if d < 0 {
			d = -d
		}
		if d > suggestMaxDist {
			continue
		}
		dist := levenshtein(name, cand, suggestMaxDist)
		if dist > suggestMaxDist {
			continue
		}
		if dist < bestDist || (dist == bestDist && cand < best) {
			best, bestDist = cand, dist
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// levenshtein computes the edit distance between a and b, short-circuiting once
// the running minimum of a row exceeds max (the caller only cares about small
// distances, so there is no need to finish a row that already lost).
func levenshtein(a, b string, max int) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// Single rolling row of distances against b.
	prev := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
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
		prev = curr
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
