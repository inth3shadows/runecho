package ir

// DefaultIgnoredPaths is the canonical list of directory names skipped during
// IR generation. All entry points (CLI buildIR, MCP liveIR, Generator default)
// use this single source so they produce identical IR for the same codebase.
var DefaultIgnoredPaths = []string{
	"node_modules", "dist", ".git", ".cursor", ".vscode", "testdata",
	// Python virtualenvs and caches
	".venv", "venv", "__pycache__", "site-packages", ".tox",
	// Go and other vendored deps
	"vendor",
}
