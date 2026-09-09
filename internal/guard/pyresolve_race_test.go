//go:build race

package guard_test

// pyRaceBuild reports whether this binary was built with the race detector.
//
// The #313 differential is the most expensive test in the repo: it stages the
// CPython stdlib, generates IR over ~131k lines, and runs the guard's scanners
// once per file per posture. At full scale under `-race -cover` it exceeded
// `go test`'s DEFAULT 10-minute per-package timeout in CI
// (`panic: test timed out after 10m0s`, internal/guard 600.037s) while every
// other package passed — a timeout, not a data race.
//
// The scale-down below is deliberately NOT a corpus reduction. A smaller corpus
// shrinks the known symbol set, which UNMASKS real filed defects (#389's
// `LC_ALL`, #390's `replace`) that the full stdlib's vocabulary happens to
// resolve — so cutting files would turn a timing problem into a red build for
// unrelated reasons. Cutting postures-per-file and the mutation budget leaves
// the corpus, the known set, and therefore the false-positive population
// exactly as they are; only the SAMPLE shrinks. The report prints every
// effective cap and budget next to its population, so a race run reads as a
// reduced run rather than a confident one.
const pyRaceBuild = true
