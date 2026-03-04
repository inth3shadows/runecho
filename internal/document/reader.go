package document

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var noiseDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, ".ai": true,
	"__pycache__": true, ".venv": true,
}

// GatherContext orchestrates all reads. irDiff is passed in from CLI flag.
func GatherContext(root, irDiff string) (*ProjectContext, error) {
	pt := detectProjectType(root)
	name, desc := extractNameDesc(root, pt)
	docTypes := loadDocTypes(root)

	readme, technical, usage := readExistingDocs(root)
	tree := buildFileTree(root)
	commits := readGitLog(root)

	ctx := &ProjectContext{
		Root:              root,
		Name:              name,
		Description:       desc,
		Type:              pt,
		FileTree:          tree,
		RecentCommits:     commits,
		IRDiff:            irDiff,
		ExistingReadme:    readme,
		ExistingTechnical: technical,
		ExistingUsage:     usage,
		DocTypes:          docTypes,
	}

	// Include source files only for create mode (no existing docs)
	if readme == "" {
		eps := detectEntrypoints(root, pt)
		ctx.SourceFiles = readSourceFiles(root, eps)
	}

	return ctx, nil
}

// loadDocTypes resolves the docs list using config hierarchy:
//  1. {root}/.ai/document.yaml (per-project override)
//  2. ~/.config/runecho/document.yaml (global user default)
//  3. Fallback: all three doc types
func loadDocTypes(root string) []string {
	if result := parseDocYAML(filepath.Join(root, ".ai", "document.yaml")); len(result) > 0 {
		return result
	}
	if home, err := os.UserHomeDir(); err == nil {
		if result := parseDocYAML(filepath.Join(home, ".config", "runecho", "document.yaml")); len(result) > 0 {
			return result
		}
	}
	return []string{"README.md", "TECHNICAL.md", "USAGE.md"}
}

// parseDocYAML extracts the docs list from a document.yaml file.
// Returns nil if file is absent or unparseable.
func parseDocYAML(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var result []string
	inDocs := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "docs:" {
			inDocs = true
			continue
		}
		if inDocs {
			if strings.HasPrefix(trimmed, "- ") {
				entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				if entry != "" {
					result = append(result, entry)
				}
			} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				break
			}
		}
	}
	return result
}

func detectProjectType(root string) ProjectType {
	checks := []struct {
		file string
		pt   ProjectType
	}{
		{"go.mod", ProjectTypeGo},
		{"package.json", ProjectTypeNode},
		{"requirements.txt", ProjectTypePython},
		{"Cargo.toml", ProjectTypeRust},
		{"pom.xml", ProjectTypeJava},
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(root, c.file)); err == nil {
			return c.pt
		}
	}
	return ProjectTypeUnknown
}

func extractNameDesc(root string, pt ProjectType) (name, desc string) {
	name = filepath.Base(root)
	desc = ""

	switch pt {
	case ProjectTypeGo:
		data, err := os.ReadFile(filepath.Join(root, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "module ") {
					mod := strings.TrimPrefix(line, "module ")
					parts := strings.Split(mod, "/")
					name = parts[len(parts)-1]
					break
				}
			}
		}
	case ProjectTypeNode:
		data, err := os.ReadFile(filepath.Join(root, "package.json"))
		if err == nil {
			// crude parse — avoid importing encoding/json just for two fields
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, `"name"`) {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						v := strings.Trim(strings.TrimSpace(parts[1]), `",`)
						if v != "" {
							name = v
						}
					}
				}
				if strings.HasPrefix(line, `"description"`) {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						desc = strings.Trim(strings.TrimSpace(parts[1]), `",`)
					}
				}
			}
		}
	}
	return name, desc
}

func buildFileTree(root string) []string {
	var paths []string
	// Walk depth 2
	entries, err := os.ReadDir(root)
	if err != nil {
		return paths
	}
	for _, e := range entries {
		if noiseDirs[e.Name()] {
			continue
		}
		rel := e.Name()
		paths = append(paths, rel)
		if e.IsDir() {
			sub, err := os.ReadDir(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			for _, se := range sub {
				if !noiseDirs[se.Name()] {
					paths = append(paths, rel+"/"+se.Name())
				}
			}
		}
	}
	return paths
}

func readGitLog(root string) string {
	out, err := exec.Command("git", "-C", root, "log", "--oneline", "-10").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func readExistingDocs(root string) (readme, technical, usage string) {
	readme = readFileTruncated(filepath.Join(root, "README.md"), 5000)
	technical = readFileTruncated(filepath.Join(root, "TECHNICAL.md"), 5000)
	usage = readFileTruncated(filepath.Join(root, "USAGE.md"), 5000)
	return
}

func readFileTruncated(path string, maxBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return truncateString(string(data), maxBytes)
}

func detectEntrypoints(root string, pt ProjectType) []string {
	candidates := map[ProjectType][]string{
		ProjectTypeGo:      {"cmd/*/main.go", "main.go"},
		ProjectTypeNode:    {"src/index.ts", "index.ts", "src/index.js", "index.js"},
		ProjectTypePython:  {"main.py", "app.py", "__main__.py"},
		ProjectTypeRust:    {"src/main.rs", "src/lib.rs"},
		ProjectTypeJava:    {"src/main/java/**/*.java"},
		ProjectTypeUnknown: {"main.go", "main.py", "index.ts", "index.js"},
	}

	pats, ok := candidates[pt]
	if !ok {
		pats = candidates[ProjectTypeUnknown]
	}

	var found []string
	for _, pat := range pats {
		matches, err := filepath.Glob(filepath.Join(root, pat))
		if err != nil || len(matches) == 0 {
			continue
		}
		for _, m := range matches {
			rel, err := filepath.Rel(root, m)
			if err == nil {
				found = append(found, rel)
			}
		}
		if len(found) >= 3 {
			break
		}
	}
	return found
}

func readSourceFiles(root string, paths []string) []SourceFile {
	var files []SourceFile
	for i, p := range paths {
		if i >= 3 {
			break
		}
		full := filepath.Join(root, p)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		content := truncateLines(string(data), 150)
		files = append(files, SourceFile{Path: p, Content: content})
	}
	return files
}

func checkDocStatus(root string, filenames []string) map[string]DocStatus {
	result := make(map[string]DocStatus, len(filenames))
	for _, fn := range filenames {
		path := filepath.Join(root, fn)
		var st DocStatus
		if _, err := os.Stat(path); err == nil {
			st.Exists = true
			st.RunMode = RunModeUpdate
		}
		if st.Exists {
			st.DirtyGit = isGitDirty(root, fn)
		}
		result[fn] = st
	}
	return result
}

func isGitDirty(root, relPath string) bool {
	out, err := exec.Command("git", "-C", root, "status", "--porcelain", relPath).Output()
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(out)) > 0
}

func truncateLines(content string, n int) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) >= n {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// formatFileTree joins the tree slice for prompt embedding.
func formatFileTree(tree []string) string {
	return strings.Join(tree, "\n")
}

// docFilename maps docType string to filename.
func docFilename(docType string) string {
	switch docType {
	case "TECHNICAL":
		return "TECHNICAL.md"
	case "USAGE":
		return "USAGE.md"
	default:
		return "README.md"
	}
}

// existingDoc returns the existing doc content for a given docType.
func existingDoc(ctx *ProjectContext, docType string) string {
	switch docType {
	case "TECHNICAL":
		return ctx.ExistingTechnical
	case "USAGE":
		return ctx.ExistingUsage
	default:
		return ctx.ExistingReadme
	}
}

// CheckDocStatus is the exported wrapper around checkDocStatus.
func CheckDocStatus(root string, filenames []string) map[string]DocStatus {
	return checkDocStatus(root, filenames)
}

// AllDocFilenames returns every possible doc filename (for pre-flight status checks).
func AllDocFilenames() []string {
	return []string{"README.md", "TECHNICAL.md", "USAGE.md"}
}

// FormatFileTree is exported for generator use.
func FormatFileTree(tree []string) string {
	return formatFileTree(tree)
}

// ExistingDoc is exported for generator use.
func ExistingDoc(ctx *ProjectContext, docType string) string {
	return existingDoc(ctx, docType)
}

// DocFilename is exported for writer use.
func DocFilename(docType string) string {
	return docFilename(docType)
}

// typeString returns ProjectType as a string.
func typeString(pt ProjectType) string {
	return string(pt)
}

// TypeString is exported.
func TypeString(pt ProjectType) string {
	return typeString(pt)
}

// describe returns the description or a fallback.
func describe(ctx *ProjectContext) string {
	if ctx.Description != "" {
		return ctx.Description
	}
	return "infer from commits and files"
}

// Describe is exported.
func Describe(ctx *ProjectContext) string {
	return describe(ctx)
}
