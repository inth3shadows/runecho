package context

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inth3shadows/runecho/internal/ir"
)

// IRProvider generates the IR CONTEXT block with relevance-scored files.
type IRProvider struct{}

func (p *IRProvider) Name() string { return "ir" }

func (p *IRProvider) Provide(req Request) (Result, error) {
	irFile := filepath.Join(req.Workspace, ".ai", "ir.json")
	data, err := ir.Load(irFile)
	if err != nil {
		// No IR file — silent skip
		return Result{Name: p.Name()}, nil
	}

	shortHash := data.RootHash
	if len(shortHash) > 12 {
		shortHash = shortHash[:12]
	}

	fileCount := len(data.Files)
	if fileCount == 0 {
		return Result{Name: p.Name()}, nil
	}

	// Read churn list and handoff for bonus scoring
	churnRaw, _ := os.ReadFile(filepath.Join(req.Workspace, ".ai", "churn-cache.txt"))
	churnList := string(churnRaw)
	handoffRaw, _ := os.ReadFile(filepath.Join(req.Workspace, ".ai", "handoff.md"))
	handoffContent := string(handoffRaw)

	promptWords := extractWords(req.Prompt)

	// Build IDF table: term → document frequency across all files
	df := buildTermDF(data)

	// Build import index: Go package path last segment → []IR file paths
	importIndex := buildImportIndex(data)

	type scoredFile struct {
		path  string
		syms  []string
		score float64
	}

	var scored []scoredFile
	N := float64(fileCount)
	for path, fileIR := range data.Files {
		syms := unique(append(fileIR.Functions, fileIR.Classes...))
		sort.Strings(syms)

		var score float64
		if len(promptWords) > 0 {
			pathLower := strings.ToLower(path)
			for _, w := range promptWords {
				idf := math.Log(1 + N/float64(1+df[w]))
				if strings.Contains(pathLower, w) {
					score += 3 * idf
				}
				for _, sym := range syms {
					if strings.Contains(strings.ToLower(sym), w) {
						score += 2 * idf
					}
				}
			}
		}
		if churnList != "" && strings.Contains(churnList, path) {
			score += 5
		}
		if handoffContent != "" && strings.Contains(handoffContent, path) {
			score += 5
		}
		scored = append(scored, scoredFile{path: path, syms: syms, score: score})
	}

	// Import propagation: files imported by high-scoring files get a bonus
	if len(promptWords) > 0 {
		// Sort descending to find top scorers
		sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
		propagationThreshold := 1.0
		scoreMap := make(map[string]int, len(scored))
		for i := range scored {
			scoreMap[scored[i].path] = i
		}
		for _, sf := range scored {
			if sf.score < propagationThreshold {
				break
			}
			fileIR := data.Files[sf.path]
			for _, imp := range fileIR.Imports {
				targets := resolveImport(imp, importIndex)
				for _, target := range targets {
					if idx, ok := scoreMap[target]; ok && target != sf.path {
						scored[idx].score += 2
					}
				}
			}
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].path < scored[j].path
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("IR CONTEXT [root_hash: %s...]:\n", shortHash))

	if len(promptWords) > 0 {
		// Prompt mode: show top files that have symbols OR high path relevance.
		// Skip no-symbol files unless their path score > 0.
		const maxShown = 10
		var shown []scoredFile
		for _, f := range scored {
			if len(shown) >= maxShown {
				break
			}
			displaySyms := filterDisplaySyms(f.syms)
			if len(displaySyms) == 0 && f.score == 0 {
				continue // pure noise: no displayable symbols and no keyword match
			}
			shown = append(shown, f)
		}
		restCount := fileCount - len(shown)

		sb.WriteString(fmt.Sprintf("Symbols in relevant files (%d/%d):\n", len(shown), fileCount))
		for _, f := range shown {
			displaySyms := filterDisplaySyms(f.syms)
			if len(displaySyms) > 0 {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", f.path, strings.Join(displaySyms, ", ")))
			} else {
				sb.WriteString(fmt.Sprintf("  %s: (no exported symbols)\n", f.path))
			}
		}
		if restCount > 0 {
			sb.WriteString(fmt.Sprintf("+ %d more files (lower relevance, run `ai-ir log` for full IR)\n", restCount))
		}
	} else {
		// No-prompt mode (post-compact): compact directory summary.
		// Group files by top-level directory, show symbol counts.
		dirFiles := make(map[string][]scoredFile)
		for _, f := range scored {
			dir := topDir(f.path)
			dirFiles[dir] = append(dirFiles[dir], f)
		}
		var dirs []string
		for d := range dirFiles {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)

		sb.WriteString(fmt.Sprintf("%d files across %d packages:\n", fileCount, len(dirs)))
		for _, dir := range dirs {
			files := dirFiles[dir]
			var symList []string
			for _, f := range files {
				symList = append(symList, filterDisplaySyms(f.syms)...)
			}
			symList = unique(symList)
			sort.Strings(symList)
			if len(symList) > 0 {
				sb.WriteString(fmt.Sprintf("  %s/ (%d files): %s\n", dir, len(files), strings.Join(symList, ", ")))
			} else {
				sb.WriteString(fmt.Sprintf("  %s/ (%d files)\n", dir, len(files)))
			}
		}
	}

	content := strings.TrimRight(sb.String(), "\n")
	return Result{
		Name:    p.Name(),
		Content: content,
		Tokens:  estimateTokens(content),
	}, nil
}

// buildTermDF computes document frequency for each term across all IR file paths and symbols.
func buildTermDF(data *ir.IR) map[string]int {
	df := make(map[string]int)
	for path, fileIR := range data.Files {
		seen := make(map[string]bool)
		for _, w := range extractWords(path) {
			if !seen[w] {
				df[w]++
				seen[w] = true
			}
		}
		for _, sym := range append(fileIR.Functions, fileIR.Classes...) {
			for _, w := range extractWords(sym) {
				if !seen[w] {
					df[w]++
					seen[w] = true
				}
			}
		}
	}
	return df
}

// buildImportIndex maps the last path segment of each IR file's directory to its file paths.
// e.g., "internal/task/task.go" dir="internal/task" → segment "task" → ["internal/task/task.go"]
func buildImportIndex(data *ir.IR) map[string][]string {
	idx := make(map[string][]string)
	for path := range data.Files {
		dir := filepath.ToSlash(filepath.Dir(path))
		parts := strings.Split(dir, "/")
		if len(parts) > 0 {
			seg := parts[len(parts)-1]
			idx[seg] = append(idx[seg], path)
		}
	}
	return idx
}

// resolveImport maps a Go import path to matching IR file paths using the last segment.
// e.g., "github.com/foo/bar/internal/task" → last segment "task" → lookup in importIndex
func resolveImport(imp string, importIndex map[string][]string) []string {
	parts := strings.Split(imp, "/")
	if len(parts) == 0 {
		return nil
	}
	seg := parts[len(parts)-1]
	return importIndex[seg]
}

// topDir returns the top-level directory segment of a file path.
// e.g., "internal/context/ir.go" → "internal/context"
// e.g., "cmd/ir/main.go" → "cmd/ir"
func topDir(path string) string {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// isTestSym returns true for Go test function names (Test*, Benchmark*, Example*).
// These are excluded from context display — they're not useful for codebase orientation.
func isTestSym(name string) bool {
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Example")
}

// filterDisplaySyms removes test symbols from a symbol list for display.
func filterDisplaySyms(syms []string) []string {
	out := syms[:0:0]
	for _, s := range syms {
		if !isTestSym(s) {
			out = append(out, s)
		}
	}
	return out
}

func extractWords(prompt string) []string {
	lower := strings.ToLower(prompt)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_'
	})
	seen := make(map[string]bool)
	var out []string
	for _, w := range words {
		if len(w) >= 3 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func unique(ss []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
