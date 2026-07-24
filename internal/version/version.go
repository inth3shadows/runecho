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

// Version is the RunEcho version string. "dev" unless stamped at build time.
var Version = "dev"

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
