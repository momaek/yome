package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// makeNotebook lays out a minimal xochitl-shaped notebook on disk.
func makeNotebook(t *testing.T, dir, uuid string, mtime time.Time) {
	t.Helper()
	files := map[string]string{
		uuid + ".content":                         `{"pages":[]}`,
		uuid + ".metadata":                        `{"visibleName":"test"}`,
		filepath.Join(uuid, "p1.rm"):              "rm-bytes-1",
		filepath.Join(uuid, "cache", "thumb.png"): "png-bytes",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(dir, uuid+".content"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentNotebookPicksNewest(t *testing.T) {
	dir := t.TempDir()
	makeNotebook(t, dir, "old-uuid", time.Now().Add(-time.Hour))
	makeNotebook(t, dir, "new-uuid", time.Now())

	got, err := CurrentNotebook(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "new-uuid" {
		t.Fatalf("CurrentNotebook = %q, want new-uuid", got)
	}
}

func TestCurrentNotebookEmptyDirErrors(t *testing.T) {
	if _, err := CurrentNotebook(t.TempDir()); err == nil {
		t.Fatal("empty dir should error")
	}
}

func TestNotebookCopiesEverything(t *testing.T) {
	dir := t.TempDir()
	makeNotebook(t, dir, "nb", time.Now())

	dest, err := Notebook(dir, "nb", filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string]string{
		"nb.content":                 `{"pages":[]}`,
		"nb.metadata":                `{"visibleName":"test"}`,
		filepath.Join("nb", "p1.rm"): "rm-bytes-1",
		filepath.Join("nb", "cache", "thumb.png"): "png-bytes",
	} {
		b, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("backup missing %s: %v", name, err)
		}
		if string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}
}

func TestNotebookUnknownUUIDErrors(t *testing.T) {
	dir := t.TempDir()
	makeNotebook(t, dir, "nb", time.Now())
	if _, err := Notebook(dir, "missing", t.TempDir()); err == nil {
		t.Fatal("unknown uuid should error")
	}
}
