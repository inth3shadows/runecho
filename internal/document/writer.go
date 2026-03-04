package document

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write persists generated docs to disk. No git operations — caller commits when ready.
// Skips any doc that is dirty in git or has empty content.
func Write(root string, docs DocSet, statuses map[string]DocStatus) error {
	for fn, content := range docs {
		st := statuses[fn]
		if st.DirtyGit {
			fmt.Fprintf(os.Stderr, "ai-document: warning: %s has uncommitted changes, skipping\n", fn)
			continue
		}
		if content == "" {
			continue
		}
		path := filepath.Join(root, fn)
		if err := writeFile(path, content); err != nil {
			fmt.Fprintf(os.Stderr, "ai-document: warning: failed to write %s: %v\n", fn, err)
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
