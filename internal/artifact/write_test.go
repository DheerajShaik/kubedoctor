package artifact

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteNewIsExclusiveAndDiscardsPartialOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result")
	partial := errors.New("fixture interrupted")
	err := WriteNew(path, func(w io.Writer) error { _, _ = io.WriteString(w, "partial"); return partial })
	if !errors.Is(err, partial) {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	write := func(w io.Writer) error { _, err := io.WriteString(w, "complete"); return err }
	if err := WriteNew(path, write); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(path, write); err == nil {
		t.Fatal("existing output replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete" {
		t.Fatal(string(data), err)
	}
}
