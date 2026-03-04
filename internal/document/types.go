package document

type ProjectType string

const (
	ProjectTypeGo      ProjectType = "go"
	ProjectTypeNode    ProjectType = "node"
	ProjectTypePython  ProjectType = "python"
	ProjectTypeRust    ProjectType = "rust"
	ProjectTypeJava    ProjectType = "java"
	ProjectTypeUnknown ProjectType = "unknown"
)

type RunMode int

const (
	RunModeCreate RunMode = iota // doc file does not exist
	RunModeUpdate                // doc file exists, IR diff available
)

type ProjectContext struct {
	Root              string
	Name              string
	Description       string
	Type              ProjectType
	FileTree          []string    // relative paths, depth 2, excludes noise dirs
	RecentCommits     string      // git log --oneline -10
	IRDiff            string      // VERIFY_SUMMARY passed in from hook (may be empty)
	ExistingReadme    string      // empty if file doesn't exist
	ExistingTechnical string
	ExistingUsage     string
	SourceFiles       []SourceFile // up to 3 key files, 150 lines each (create mode only)
	DocTypes          []string     // filenames to generate: e.g. ["README.md"] or ["README.md","TECHNICAL.md","USAGE.md"]
}

type SourceFile struct {
	Path    string
	Content string
}

// DocSet maps doc filename → generated content. Empty string = skip write.
type DocSet map[string]string

// DocStatus tracks what should happen to each doc file.
type DocStatus struct {
	Exists   bool // file exists on disk
	DirtyGit bool // has uncommitted local changes (skip write)
	RunMode  RunMode
}
