package guard

import (
	"os"
	"sort"
	"strings"
)

// SeedState is every pre-hunk seed tracker's state at the START of a line: the
// unterminated multi-line string delimiter (Open, #145/#178) and, for Python
// only, the four depths a hunk-only scanner cannot recover from its own lines —
// dict nesting for pyBraceDepth (#289), general bracket depth for
// PyDeclaredNames (#294), LocallyBoundNames' paren-only def-signature depth
// (#294), and PyParamNames' own all-bracket def-signature depth (#294). The two
// signature depths are DIFFERENT rules; see PyParamSigDepthBefore's doc.
//
// Before #335 each field had its own hand-kept per-line rule in two places (a
// pre-commit prefix builder and a hook *Before walker), ten copies in all, and
// every consumer read and walked the file separately. advanceSeed is now the
// one rule and seedTable/SeedStatesAt the only two walks.
type SeedState struct {
	Open                             string
	Brace, Bracket, DefSig, ParamSig int
}

// advanceSeed threads st through one line and returns the state at the start
// of the next. It is the single per-line rule for every seed: one
// stripLiteralsBraces call feeds all five trackers. Outside Python only Open is
// tracked; the depths are never consulted there.
func advanceSeed(lang Lang, st SeedState, line string) SeedState {
	scan, braceScan, open := stripLiteralsBraces(lang, line, st.Open)
	next := SeedState{Open: open}
	if lang != LangPython {
		return next
	}

	// Dict nesting: the same accounting extractRefs' own per-line advance uses,
	// including the clamp and the f-string neutralisation (#291) — a seed
	// computed any other way would hand the run a depth its own tracking would
	// never produce.
	next.Brace = pyLineCtx{scan: braceScan, base: st.Brace}.depthAtEnd()

	// General bracket depth, on the f-string-neutralised braceScan.
	next.Bracket = st.Bracket + pyBracketDelta(braceScan)
	if next.Bracket < 0 {
		next.Bracket = 0
	}

	// LocallyBoundNames' def-signature depth: parens only, on the plain scan.
	defSig := st.DefSig
	if defSig > 0 {
		pyConsumeParens(scan, &defSig)
	} else if loc := rePyDefOpen.FindStringIndex(scan); loc != nil {
		defSig = 1
		pyConsumeParens(scan[loc[1]:], &defSig)
	}
	next.DefSig = defSig

	// PyParamNames' def-signature depth: ALL of ()/[]/{}, so a multi-line
	// default value's own bracket keeps the signature open.
	paramSig := st.ParamSig
	if paramSig > 0 {
		paramSig += pyBracketDelta(braceScan)
		if paramSig <= 0 {
			paramSig = 0
		}
	} else if loc := rePyDefParamOpen.FindStringSubmatchIndex(scan); loc != nil {
		paramSig = 1 + pyBracketDelta(braceScan[loc[2]:loc[3]])
		if paramSig <= 0 {
			paramSig = 0
		}
	}
	next.ParamSig = paramSig
	return next
}

// seedTable holds the seed state at the start of every line of one file:
// prefix[k] is the state at the start of 1-based line k+1, and the final entry
// is the state past the last line.
type seedTable struct {
	lang   Lang
	prefix []SeedState
}

func buildSeedTable(lang Lang, lines []string) *seedTable {
	prefix := make([]SeedState, len(lines)+1)
	var st SeedState
	for i, ln := range lines {
		prefix[i] = st
		st = advanceSeed(lang, st, ln)
	}
	prefix[len(lines)] = st
	return &seedTable{lang: lang, prefix: prefix}
}

// at returns the state at the start of 1-based lineNo, clamped into range so an
// out-of-range line yields the nearest end's state rather than a panic.
func (t *seedTable) at(lineNo int) SeedState {
	idx := lineNo - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(t.prefix) {
		idx = len(t.prefix) - 1
	}
	return t.prefix[idx]
}

// readSeedFile is the seed read, a seam so a test can count reads.
var readSeedFile = os.ReadFile

// maxSeedFileBytes caps the file read for pre-hunk seeding. Past it, seeding is
// skipped (fall back to hunk-only scanning) rather than reading an unbounded
// blob into memory on the pre-commit path.
const maxSeedFileBytes = 8 << 20 // 8 MiB

// loadSeedTable reads absPath once and builds its seed table. It returns nil —
// no seeding, hunk-only scanning — when the file can't be read or exceeds
// maxSeedFileBytes: fail-open, matching the guard's degraded-input posture. The
// working-tree file is read; for the unchanged context above a hunk it matches
// the staged content the diff came from (an unstaged edit to that context is a
// rare corner and stays fail-open).
func loadSeedTable(lang Lang, absPath string) *seedTable {
	data, err := readSeedFile(absPath)
	if err != nil || len(data) > maxSeedFileBytes {
		return nil
	}
	return buildSeedTable(lang, strings.Split(string(data), "\n"))
}

// SeedStatesAt returns the seed state at the START of fileLines[idx] for each
// requested idx, from ONE walk of fileLines up to the largest of them — the
// hook path's counterpart to seedTable, for a file it already holds in memory
// and needs only at a few matched block positions. Each idx is clamped into
// [0, len(fileLines)] (0 and anything below is the zero state; past the end is
// the state after the last line); the result is keyed by the idx as given.
// fileLines must be a whole file's contiguous lines.
func SeedStatesAt(lang Lang, fileLines []AddedLine, idxs []int) map[int]SeedState {
	out := make(map[int]SeedState, len(idxs))
	if len(idxs) == 0 {
		return out
	}
	clamp := func(idx int) int {
		if idx < 0 {
			return 0
		}
		if idx > len(fileLines) {
			return len(fileLines)
		}
		return idx
	}
	targets := make([]int, 0, len(idxs))
	for _, idx := range idxs {
		targets = append(targets, clamp(idx))
	}
	sort.Ints(targets)
	at := make(map[int]SeedState, len(targets))
	var st SeedState
	pos := 0 // st is the state at the start of fileLines[pos]
	for _, tgt := range targets {
		for ; pos < tgt; pos++ {
			st = advanceSeed(lang, st, fileLines[pos].Text)
		}
		at[tgt] = st
	}
	for _, idx := range idxs {
		out[idx] = at[clamp(idx)]
	}
	return out
}

// seedStateBefore is SeedStatesAt for a single position.
func seedStateBefore(lang Lang, fileLines []AddedLine, idx int) SeedState {
	return SeedStatesAt(lang, fileLines, []int{idx})[idx]
}

// PrepareSeeds reads each AbsPath diff's file once and attaches its seed table,
// so every check that seeds from it shares one read and one walk instead of
// each re-reading the file (#295). Call it after setting AbsPath; a diff it
// skips (no AbsPath, unknown language) keeps reading on demand.
func PrepareSeeds(diffs []FileDiff) {
	for i := range diffs {
		fd := &diffs[i]
		if fd.AbsPath == "" {
			continue
		}
		lang := LangFor(fd.Path)
		if lang == LangUnknown {
			continue
		}
		fd.seeds = loadSeedTable(lang, fd.AbsPath)
		fd.seedsPrepared = true
	}
}

// seedTable returns fd's AbsPath seed table for lang: the one PrepareSeeds
// attached when it was built for the same language (or its failed read, nil),
// else a fresh read.
func (fd FileDiff) seedTable(lang Lang) *seedTable {
	if fd.seedsPrepared && (fd.seeds == nil || fd.seeds.lang == lang) {
		return fd.seeds
	}
	return loadSeedTable(lang, fd.AbsPath)
}
