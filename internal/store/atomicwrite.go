package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to path by creating a unique sibling temp file in
// path's directory, writing and closing it, then renaming it over path — atomic on
// the same filesystem on Linux/macOS, so a crash mid-write or a concurrent writer
// can never leave a half-written, unparseable file in place. The temp file is
// created 0600 (owner-only, from os.CreateTemp) and removed on any error before the
// rename, leaving the existing file untouched on failure.
//
// The temp name is unique per call (base + ".tmp-*"), so two concurrent writers
// never share a temp and each rename carries its own complete content; that pattern
// is also what IR.Save's orphan-temp reaper globs for (path + ".tmp-*"). The parent
// directory must already exist — a caller that can't assume it should MkdirAll first.
func AtomicWriteFile(path string, data []byte) error {
	tmpF, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmp := tmpF.Name()
	if _, err := tmpF.Write(data); err != nil {
		tmpF.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpF.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup; the real file is untouched
		return fmt.Errorf("rename temp file over %s: %w", filepath.Base(path), err)
	}
	return nil
}
