package context

import (
	"fmt"
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

	// Score files by relevance to the current prompt
	promptWords := extractWords(req.Prompt)

	type scoredFile struct {
		path  string
		syms  []string
		score int
	}

	var scored []scoredFile
	for path, fileIR := range data.Files {
		syms := unique(append(fileIR.Functions, fileIR.Classes...))
		sort.Strings(syms)

		score := 0
		if len(promptWords) > 0 {
			pathLower := strings.ToLower(path)
			for _, w := range promptWords {
				if strings.Contains(pathLower, w) {
					score += 3
				}
				for _, sym := range syms {
					if strings.Contains(strings.ToLower(sym), w) {
						score += 2
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

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].path < scored[j].path
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("IR CONTEXT [root_hash: %s...]:\n", shortHash))

	const maxShown = 15
	shown := scored
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	restCount := len(scored) - len(shown)

	if len(promptWords) > 0 {
		sb.WriteString(fmt.Sprintf("Symbols in relevant files (%d/%d):\n", len(shown), fileCount))
		for _, f := range shown {
			if len(f.syms) > 0 {
				sb.WriteString(fmt.Sprintf("  %s: %s\n", f.path, strings.Join(f.syms, ", ")))
			} else {
				sb.WriteString(fmt.Sprintf("  %s: (no exported symbols)\n", f.path))
			}
		}
	} else {
		// Flat dump: file list + all symbols
		var allPaths []string
		for _, f := range scored {
			allPaths = append(allPaths, f.path)
		}
		var allSyms []string
		for _, f := range scored {
			allSyms = append(allSyms, f.syms...)
		}
		allSyms = unique(allSyms)
		sort.Strings(allSyms)
		sb.WriteString(fmt.Sprintf("%d files — %s\n", fileCount, strings.Join(allPaths, ", ")))
		sb.WriteString(fmt.Sprintf("Symbols (%d): %s\n", len(allSyms), strings.Join(allSyms, ", ")))
	}

	if restCount > 0 {
		sb.WriteString(fmt.Sprintf("+ %d more files (lower relevance, run `ai-ir log` for full IR)\n", restCount))
	}

	content := strings.TrimRight(sb.String(), "\n")
	return Result{
		Name:    p.Name(),
		Content: content,
		Tokens:  estimateTokens(content),
	}, nil
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
