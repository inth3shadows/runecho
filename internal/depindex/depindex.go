// Package depindex resolves an external dependency's real exported-symbol set so
// the guard can validate a module-qualified call (`pl.pearsonr(...)`) against the
// package that is actually installed — the external half of the qualified-call
// gap that internal/guard/qualified.go closed for same-repo packages (#176).
//
// The whole point of RunEcho is zero false positives, and a dependency index is
// the most dangerous thing that can feed the guard: an index that is merely
// INCOMPLETE looks exactly like a hallucination ("symbol not in the export set")
// while being a valid call. So resolution is deliberately TRI-state, and the
// guard may act on exactly one of the three:
//
//	Unknown  — package not found, no confident environment, unreadable → ABSTAIN
//	Partial  — found, but its export surface is not exhaustively enumerable
//	           (lazy __getattr__, star-imports, computed __all__) → ABSTAIN
//	Resolved — complete and trustworthy → the guard MAY flag an absent symbol
//
// The invariant that makes this safe: no code path constructs Resolved from a
// truncated, capped, or best-effort walk. Every early return on error, size cap,
// or ambiguity yields Unknown or Partial. A miss is free; a false positive is not.
package depindex

import "fmt"

// Resolution is the tri-state trust level of a dependency lookup. Only Resolved
// permits the guard to flag; see the package doc for why this is not a bool.
type Resolution int

const (
	// Unknown means the package could not be located or read at all. The guard
	// learns nothing and must abstain.
	Unknown Resolution = iota
	// Partial means the package was located but something about it makes its
	// export surface non-enumerable by static means. Abstain — the missing
	// names are precisely the ones that would false-positive.
	Partial
	// Resolved means Exports is a complete (in practice, over-approximate)
	// account of what the package binds. A symbol absent from it is absent from
	// the package.
	Resolved
)

func (r Resolution) String() string {
	switch r {
	case Unknown:
		return "unknown"
	case Partial:
		return "partial"
	case Resolved:
		return "resolved"
	}
	return fmt.Sprintf("Resolution(%d)", int(r))
}

// PackageSymbols is one dependency's resolved export surface.
//
// Exports is meaningful only when Res == Resolved. It is deliberately an
// OVER-approximation of the package's public API — it unions the declared
// __all__ with every top-level binding in the module, including private and
// re-exported names. Over-approximating biases every disagreement toward
// "don't flag", which is the correct direction for a zero-FP tool.
type PackageSymbols struct {
	Res     Resolution
	Exports map[string]struct{}
	// Reason explains a non-Resolved result (and is empty when Resolved). It is
	// surfaced under RUNECHO_DEBUG so an abstain is diagnosable rather than
	// silent — "why didn't the guard catch this" is otherwise unanswerable.
	Reason string
}

// Has reports whether name is present in a RESOLVED package's export set. It
// returns false for Unknown/Partial, so callers that forget to check Res still
// fail toward "not found" rather than toward a flag — but callers MUST check
// Res explicitly, because "not found" is exactly what triggers a violation.
func (ps PackageSymbols) Has(name string) bool {
	if ps.Res != Resolved {
		return false
	}
	_, ok := ps.Exports[name]
	return ok
}

// Index resolves a module/import path to its export surface. Implementations are
// per-ecosystem (Python first; Go and JS/TS follow in later phases of #175).
//
// Lookup must be safe for concurrent use and must never block long enough to
// threaten the guard's ~12ms edit-time budget: an implementation that cannot
// answer cheaply returns Unknown rather than doing expensive work.
type Index interface {
	Lookup(module string) PackageSymbols
}

// unknown is the canonical abstain result, used at every failure return so the
// reason string is the only thing that varies.
func unknown(format string, args ...any) PackageSymbols {
	return PackageSymbols{Res: Unknown, Reason: fmt.Sprintf(format, args...)}
}

// partial is the canonical "found but not enumerable" result.
func partial(format string, args ...any) PackageSymbols {
	return PackageSymbols{Res: Partial, Reason: fmt.Sprintf(format, args...)}
}
