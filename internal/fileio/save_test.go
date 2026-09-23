package fileio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/textfile"
)

func TestSaveFile(t *testing.T) {
	tests := []struct {
		name     string
		existing []byte
		oldMode  os.FileMode
		path     string
		lines    []string
		format   textfile.Format
		mode     os.FileMode
		want     string
		wantMode os.FileMode
	}{
		{name: "overwrite keeps exec mode", existing: []byte("old\n"), oldMode: 0o755, path: "run.sh",
			lines: []string{"#!/bin/sh", "echo hi"}, format: textfile.Format{EOL: textfile.LF, FinalNewline: true},
			mode: 0o755, want: "#!/bin/sh\necho hi\n", wantMode: 0o755},
		{name: "private mode", existing: []byte("x"), oldMode: 0o600, path: "secret.txt",
			lines: []string{"a"}, mode: 0o600, want: "a", wantMode: 0o600},
		{name: "crlf bom", path: "win.txt", lines: []string{"a", "b"},
			format: textfile.Format{EOL: textfile.CRLF, BOM: true, FinalNewline: true},
			mode:   0o644, want: "\xef\xbb\xbfa\r\nb\r\n", wantMode: 0o644},
		{name: "new file in new subdir default mode", path: "a/b/c/new.go", lines: []string{"package c"},
			format: textfile.Format{FinalNewline: true}, want: "package c\n", wantMode: DefaultMode},
		{name: "empty", path: "empty", mode: 0o644, want: "", wantMode: 0o644},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, filepath.FromSlash(tt.path))
			if tt.existing != nil {
				if err := os.WriteFile(p, tt.existing, tt.oldMode); err != nil {
					t.Fatal(err)
				}
				os.Chmod(p, tt.oldMode)
			}
			if err := SaveFile(p, tt.lines, tt.format, tt.mode); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
			st, _ := os.Stat(p)
			if st.Mode().Perm() != tt.wantMode {
				t.Errorf("mode = %v, want %v", st.Mode().Perm(), tt.wantMode)
			}
			assertNoTemp(t, filepath.Dir(p), filepath.Base(p))
		})
	}
}

func TestSaveDocMixed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.txt")
	in := []byte("a\r\nb\nc\r\n")
	d := textfile.Parse(in)
	if err := SaveDoc(p, d, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(in) {
		t.Errorf("got %q", got)
	}
}

func TestSaveFileFailureBeforeRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(p, []byte("original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("disk on fire")
	var sawTemp string
	beforeRename = func(tmp string) error {
		sawTemp = tmp
		if _, err := os.Stat(tmp); err != nil {
			t.Errorf("temp missing before rename: %v", err)
		}
		return injected
	}
	defer func() { beforeRename = nil }()

	err := SaveFile(p, []string{"new"}, textfile.Format{FinalNewline: true}, 0o640)
	if !errors.Is(err, injected) || !outcome.Is(err, outcome.Internal) {
		t.Fatalf("err = %v", err)
	}
	if filepath.Dir(sawTemp) != dir {
		t.Errorf("temp %q not in target dir", sawTemp)
	}
	if got, _ := os.ReadFile(p); string(got) != "original\n" {
		t.Errorf("original changed: %q", got)
	}
	assertNoTemp(t, dir, "keep.txt")
}

func TestSaveFileErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
	}{
		{"parent is a file", filepath.Join(blocker, "x.txt")},
		{"target is a dir", dir},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SaveFile(tt.path, []string{"x"}, textfile.Format{}, 0)
			if !outcome.Is(err, outcome.Internal) {
				t.Fatalf("err = %v", err)
			}
			if e, _ := outcome.As(err); filepath.IsAbs(e.Message) || strings.Contains(e.Message, dir) {
				t.Errorf("message leaks path: %q", e.Message)
			}
		})
	}
	assertNoTemp(t, dir, "file")
}

func assertNoTemp(t *testing.T, dir string, keep ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, k := range keep {
		allowed[k] = true
	}
	for _, e := range entries {
		if !allowed[e.Name()] && !e.IsDir() {
			t.Errorf("unexpected file left behind: %s", e.Name())
		}
	}
}
