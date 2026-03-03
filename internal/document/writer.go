package document

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write persists generated docs to disk. No git operations — caller commits when ready.
// personal/unknown: writes README.md only (if not dirty)
// work: writes README.md, TECHNICAL.md, USAGE.md (each skipped if dirty or empty)
func Write(root string, docs *DocSet, statuses map[string]DocStatus, mode Mode) error {
	type docEntry struct {
		filename string
		content  string
	}

	entries := []docEntry{
		{"README.md", docs.Readme},
	}
	if mode == ModeWork {
		entries = append(entries,
			docEntry{"TECHNICAL.md", docs.Technical},
			docEntry{"USAGE.md", docs.Usage},
		)
	}

	for _, e := range entries {
		st := statuses[e.filename]
		if st.DirtyGit {
			fmt.Fprintf(os.Stderr, "ai-document: warning: %s has uncommitted changes, skipping\n", e.filename)
			continue
		}
		if e.content == "" {
			continue
		}
		path := filepath.Join(root, e.filename)
		if err := writeFile(path, e.content); err != nil {
			fmt.Fprintf(os.Stderr, "ai-document: warning: failed to write %s: %v\n", e.filename, err)
		}
	}

	return nil
}

func writeFile(path, content string) error {
	if content == "" {
		return nil
	}
	return os.WriteFile(path, []byte(content+"\n"), 0644)
}
