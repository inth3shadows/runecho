package parser

import (
	"regexp"
	"sort"
	"strings"
)

// PythonParser implements shallow parsing for .py files.
// Uses regex patterns — not semantically correct, but deterministic.
// Extracts imports, top-level functions, classes, and __all__ exports.
type PythonParser struct{}

func NewPythonParser() *PythonParser { return &PythonParser{} }

var (
	// import foo or import foo.bar
	pyImportRegex = regexp.MustCompile(`^import\s+([\w.]+)`)

	// from foo import bar (captures the module path)
	pyFromImportRegex = regexp.MustCompile(`^from\s+([\w.]+)\s+import\s+`)

	// def func_name(
	pyFuncRegex = regexp.MustCompile(`^def\s+(\w+)\s*\(`)

	// class ClassName(
	pyClassRegex = regexp.MustCompile(`^class\s+(\w+)[\s:(]`)

	// __all__ = ["foo", "bar"] or ('foo', 'bar')
	pyAllRegex = regexp.MustCompile(`__all__\s*=\s*[\[\(]([^\]\)]+)[\]\)]`)

	// individual quoted names inside __all__
	pyAllItemRegex = regexp.MustCompile(`["'](\w+)["']`)
)

func (p *PythonParser) SupportsExtension(ext string) bool {
	return ext == ".py"
}

func (p *PythonParser) Parse(source string) (FileStructure, error) {
	var imports, functions, classes []string
	importSet := make(map[string]bool)

	lines := strings.Split(source, "\n")
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		if m := pyImportRegex.FindStringSubmatch(line); m != nil {
			mod := strings.Split(m[1], ".")[0] // top-level package only
			if !importSet[mod] {
				imports = append(imports, m[1])
				importSet[mod] = true
			}
			continue
		}

		if m := pyFromImportRegex.FindStringSubmatch(line); m != nil {
			if !importSet[m[1]] {
				imports = append(imports, m[1])
				importSet[m[1]] = true
			}
			continue
		}

		if m := pyFuncRegex.FindStringSubmatch(line); m != nil {
			// Skip private/dunder functions
			if !strings.HasPrefix(m[1], "_") {
				functions = append(functions, m[1])
			}
			continue
		}

		if m := pyClassRegex.FindStringSubmatch(line); m != nil {
			classes = append(classes, m[1])
			continue
		}
	}

	// Extract __all__ exports from full source
	var exports []string
	if m := pyAllRegex.FindStringSubmatch(source); m != nil {
		items := pyAllItemRegex.FindAllStringSubmatch(m[1], -1)
		for _, item := range items {
			exports = append(exports, item[1])
		}
	}

	sort.Strings(imports)
	sort.Strings(functions)
	sort.Strings(classes)
	sort.Strings(exports)

	return FileStructure{
		Imports:   imports,
		Functions: functions,
		Classes:   classes,
		Exports:   exports,
	}, nil
}
