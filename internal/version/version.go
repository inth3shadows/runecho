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
