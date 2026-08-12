// Package version exposes the single source of truth for the RunEcho build
// version. Both binaries (runecho-mcp, runecho-guard) read Version from here, so
// the version a client sees can never drift between them.
//
// Version defaults to "dev" and is overridden at build time by the installer via
//
//	-ldflags "-X github.com/inth3shadows/runecho/internal/version.Version=$(git describe --tags)"
//
// A plain `go build` (no stamp) reports "dev" — honest about being an unstamped
// local build rather than asserting a stale release number.
package version

import (
	"regexp"
	"strings"
)

// Version is the RunEcho version string. "dev" unless stamped at build time.
var Version = "dev"

// semverCoreRE matches the X.Y.Z core of a version string, dropping any
// `-N-gsha`/`-dirty` build suffix. A post-tag build reports v0.16.1-3-gabc1234,
// and comparing full describe strings with sort -V orders such suffixes
// inconsistently — comparing only the core makes "ahead of the tag" read as
// "not behind".
//
// The leading `v` is OPTIONAL because the two build channels stamp differently:
// install.sh uses `git describe --tags` → "v0.17.4", but goreleaser stamps
// `{{ .Version }}` → "0.17.4" (goreleaser strips the v). Requiring the v made
// version comparisons silently inert for a goreleaser binary. Inputs are
// always a stamp or `--version` output, never free text, so the optional v
// cannot match a stray "goX.Y.Z"-style substring in practice.
var semverCoreRE = regexp.MustCompile(`v?[0-9]+\.[0-9]+\.[0-9]+`)

// SemverCore extracts the first vX.Y.Z occurrence from s, or "" if none.
func SemverCore(s string) string {
	return semverCoreRE.FindString(s)
}

// ParseSemver parses "vX.Y.Z" into [3]int. ok=false on any malformed input.
func ParseSemver(s string) ([3]int, bool) {
	var out [3]int
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n := 0
		if p == "" {
			return out, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return out, false
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out, true
}

// SemverLess reports whether core a is strictly semver-less than core b. Both
// must be vX.Y.Z cores (as returned by SemverCore); a malformed or empty input
// yields false, so an unreadable version never triggers a downgrade or a rebuild.
func SemverLess(a, b string) bool {
	ap, aok := ParseSemver(a)
	bp, bok := ParseSemver(b)
	if !aok || !bok {
		return false
	}
	for i := 0; i < 3; i++ {
		if ap[i] != bp[i] {
			return ap[i] < bp[i]
		}
	}
	return false
}

// Canonical normalizes a version string to the `vX.Y.Z[...]` form. It exists
// because the two build channels stamp Version differently: install.sh uses
// `git describe --tags` → "v0.17.4", but goreleaser stamps `{{ .Version }}` →
// "0.17.4" (goreleaser strips the leading v). Left unnormalized, the same
// release is recorded under two labels, which splits guardstats' per-version
// bucketing and silently suppresses the fpreport release gate (#233).
//
// The rule is minimal on purpose: a value beginning with a digit is a bare
// semver core missing its v, so prepend one. Everything else — an already
// v-prefixed string, "dev", an empty stamp — is returned unchanged, so this
// never invents a version for an unstamped build.
func Canonical(s string) string {
	if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
		return "v" + s
	}
	return s
}
