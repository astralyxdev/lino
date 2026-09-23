package fileio

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/paths"
	"github.com/astralyx/lino/internal/textfile"
)

func TestLoadFile(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "root")
	mustMkdir(t, filepath.Join(dir, "src"))
	mustMkdir(t, filepath.Join(dir, ".lino"))
	mustMkdir(t, filepath.Join(base, "out"))

	crlfBOM := append(append([]byte{}, textfile.BOM...), "one\r\ntwo\r\nthree"...)
	files := map[string]struct {
		data []byte
		mode os.FileMode
	}{
		"src/a.go":     {[]byte("package a\n\nfunc A() {}\n"), 0o644},
		"src/win.txt":  {crlfBOM, 0o600},
		"src/run.sh":   {[]byte("#!/bin/sh\n"), 0o755},
		"src/empty":    {nil, 0o644},
		"src/bin":      {[]byte("a\x00b"), 0o644},
		"src/latin1":   {[]byte("caf\xe9\n"), 0o644},
		"src/big":      {bytes.Repeat([]byte("x"), 101), 0o644},
		".lino/config": {[]byte("read.lines = 10\n"), 0o644},
	}
	for name, f := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, f.data, f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "out", "secret"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustLink(t, filepath.Join(base, "out", "secret"), filepath.Join(dir, "escape"))
	mustLink(t, "src/a.go", filepath.Join(dir, "alias"))
	mustLink(t, ".lino/config", filepath.Join(dir, "meta"))

	root, err := paths.NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		cwd     string
		path    string
		outcome outcome.Outcome
		rel     string
		lines   []string
		format  textfile.Format
		mode    os.FileMode
	}{
		{name: "lf", path: "src/a.go", outcome: outcome.OK, rel: "src/a.go",
			lines:  []string{"package a", "", "func A() {}"},
			format: textfile.Format{EOL: textfile.LF, FinalNewline: true}, mode: 0o644},
		{name: "crlf bom no final newline", path: "src/win.txt", outcome: outcome.OK, rel: "src/win.txt",
			lines:  []string{"one", "two", "three"},
			format: textfile.Format{EOL: textfile.CRLF, BOM: true}, mode: 0o600},
		{name: "exec mode", path: "src/run.sh", outcome: outcome.OK, rel: "src/run.sh",
			lines:  []string{"#!/bin/sh"},
			format: textfile.Format{EOL: textfile.LF, FinalNewline: true}, mode: 0o755},
		{name: "empty", path: "src/empty", outcome: outcome.OK, rel: "src/empty",
			format: textfile.Format{EOL: textfile.LF}, mode: 0o644},
		{name: "relative to cwd", cwd: filepath.Join(root.Path(), "src"), path: "a.go", outcome: outcome.OK, rel: "src/a.go",
			lines:  []string{"package a", "", "func A() {}"},
			format: textfile.Format{EOL: textfile.LF, FinalNewline: true}, mode: 0o644},
		{name: "in-root symlink", path: "alias", outcome: outcome.OK, rel: "alias",
			lines:  []string{"package a", "", "func A() {}"},
			format: textfile.Format{EOL: textfile.LF, FinalNewline: true}, mode: 0o644},
		{name: "missing", path: "src/nope.go", outcome: outcome.NotFound},
		{name: "missing dir", path: "nodir/x", outcome: outcome.NotFound},
		{name: "directory", path: "src", outcome: outcome.Refused},
		{name: "root", path: ".", outcome: outcome.Refused},
		{name: "binary nul", path: "src/bin", outcome: outcome.Refused},
		{name: "binary latin1", path: "src/latin1", outcome: outcome.Refused},
		{name: "too large", path: "src/big", outcome: outcome.Refused},
		{name: "traversal", path: "../out/secret", outcome: outcome.Refused},
		{name: "absolute outside", path: filepath.Join(base, "out", "secret"), outcome: outcome.Refused},
		{name: "symlink escape", path: "escape", outcome: outcome.Refused},
		{name: "meta dir file", path: ".lino/config", outcome: outcome.Refused},
		{name: "symlink into meta", path: "meta", outcome: outcome.Refused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := LoadFile(root, tt.cwd, tt.path, 100)
			if got := outcome.Of(err); got != tt.outcome {
				t.Fatalf("outcome = %v (%v), want %v", got, err, tt.outcome)
			}
			if err != nil {
				if e, _ := outcome.As(err); bytes.Contains([]byte(e.Message), []byte(base)) && !filepath.IsAbs(tt.path) {
					t.Errorf("message leaks absolute path: %q", e.Message)
				}
				return
			}
			if f.Path != tt.rel {
				t.Errorf("Path = %q, want %q", f.Path, tt.rel)
			}
			if !reflect.DeepEqual(f.Lines(), tt.lines) {
				t.Errorf("Lines = %q, want %q", f.Lines(), tt.lines)
			}
			if f.Format() != tt.format {
				t.Errorf("Format = %+v, want %+v", f.Format(), tt.format)
			}
			if f.Mode != tt.mode {
				t.Errorf("Mode = %v, want %v", f.Mode, tt.mode)
			}
			if f.Size != int64(len(f.Data)) || f.ModTime.IsZero() {
				t.Errorf("Size = %d, len(Data) = %d, ModTime = %v", f.Size, len(f.Data), f.ModTime)
			}
			if !bytes.Equal(f.Doc.Bytes(), f.Data) {
				t.Errorf("round trip mismatch: %q vs %q", f.Doc.Bytes(), f.Data)
			}
		})
	}
}

func TestLoadFileCRLFBOMBytes(t *testing.T) {
	dir := t.TempDir()
	want := append(append([]byte{}, textfile.BOM...), "a\r\nb\r\n"...)
	if err := os.WriteFile(filepath.Join(dir, "w.txt"), want, 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := paths.NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(root, "", "w.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Lines(), []string{"a", "b"}) {
		t.Fatalf("Lines = %q", f.Lines())
	}
	if got := (textfile.Format{EOL: textfile.CRLF, BOM: true, FinalNewline: true}); f.Format() != got {
		t.Fatalf("Format = %+v", f.Format())
	}
	if !bytes.Equal(f.Data, want) || !bytes.Equal(f.Doc.Bytes(), want) {
		t.Fatal("bytes not preserved")
	}
	if f.Size != int64(len(want)) {
		t.Fatalf("Size = %d", f.Size)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustLink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}
