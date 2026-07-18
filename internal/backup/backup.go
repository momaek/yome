// Package backup copies a notebook's on-disk files byte-for-byte before a
// destructive action. It never parses the .rm format — the format is still
// evolving and a backup that needs a parser is a backup that can fail
// (plan 4.5: pure copy, no interpretation).
package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultDataDir is where xochitl keeps notebooks.
const DefaultDataDir = "/home/root/.local/share/remarkable/xochitl"

// DefaultBackupDir is where pre-erase backups land.
const DefaultBackupDir = "/home/root/.cache/rm2-ai/backups"

// CurrentNotebook returns the UUID of the notebook most recently written to
// disk — a heuristic for "the one open on screen": xochitl persists strokes
// as the user draws, so the open notebook is the freshest one.
//
// The caveat is honest: strokes injected seconds ago may not be flushed yet,
// and a device that has been idle in a menu keeps stale mtimes. Callers log
// the chosen UUID so a wrong pick is visible.
func CurrentNotebook(dataDir string) (uuid string, err error) {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return "", fmt.Errorf("read xochitl data dir %s: %w", dataDir, err)
	}
	var newest time.Time
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".content") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
			uuid = strings.TrimSuffix(name, ".content")
		}
	}
	if uuid == "" {
		return "", fmt.Errorf("no .content files in %s — is this a xochitl data dir?", dataDir)
	}
	return uuid, nil
}

// Notebook copies every file belonging to the notebook (uuid.* and the uuid/
// page directory) into a fresh timestamped directory under backupDir, and
// returns that directory's path.
func Notebook(dataDir, uuid, backupDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dataDir, uuid+"*"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("notebook %s has no files in %s", uuid, dataDir)
	}

	dest := filepath.Join(backupDir, fmt.Sprintf("%s-%s", uuid, time.Now().Format("20060102-150405")))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	for _, src := range matches {
		if err := copyTree(src, filepath.Join(dest, filepath.Base(src))); err != nil {
			return "", fmt.Errorf("back up %s: %w", src, err)
		}
	}
	return dest, nil
}

// copyTree copies a file or directory recursively, plain byte copies only.
func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst, info.Mode())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
