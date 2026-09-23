package harness

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Corpus describes the files copied into a benchmark root.
type Corpus struct {
	Source string `json:"source"`
	Pin    string `json:"pin,omitempty"` // e.g. golang/go@<commit>, from bench/corpus.sh
	Files  int    `json:"files"`
	Lines  int    `json:"lines"`
	Bytes  int64  `json:"bytes"`
	Digest string `json:"digest"` // sha256 over path and content of every copied file
}

// maxFileBytes skips generated giants, as the index spike did.
const maxFileBytes = 4 << 20

// CopyCorpus copies text files into dst, in lexical path order, stopping once
// maxLines lines are copied (0: no cap). src is a directory, or a .tar.gz
// whose single top-level directory is stripped (a GitHub archive), with sub
// selecting a directory inside it ("" for all). Hidden files and
// directories and testdata are skipped; so are binary, non-UTF-8 and files
// over 4 MB. The same tree and cap give the same Digest from either form.
func CopyCorpus(src, sub, dst string, maxLines int) (Corpus, error) {
	c := Corpus{Source: src, Pin: readPin(src)}
	if sub != "" {
		c.Source += "#" + sub
	}
	var files []corpusFile
	var err error
	if strings.HasSuffix(src, ".tar.gz") || strings.HasSuffix(src, ".tgz") {
		files, err = tarFiles(src, sub)
	} else {
		files, err = dirFiles(filepath.Join(src, filepath.FromSlash(sub)))
	}
	if err != nil {
		return c, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	h := sha256.New()
	for _, f := range files {
		if maxLines > 0 && c.Lines >= maxLines {
			break
		}
		b, err := f.read()
		if err != nil {
			return c, err
		}
		if len(b) > maxFileBytes || bytes.IndexByte(b, 0) >= 0 || !utf8.Valid(b) {
			continue
		}
		out := filepath.Join(dst, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return c, err
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return c, err
		}
		h.Write([]byte(f.rel))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
		c.Files++
		c.Lines += countLines(b)
		c.Bytes += int64(len(b))
	}
	c.Digest = hex.EncodeToString(h.Sum(nil))
	return c, nil
}

type corpusFile struct {
	rel  string // slash-separated, relative to the corpus root
	read func() ([]byte, error)
}

// skipped reports whether a path element excludes a file from the corpus.
func skipped(elem string, dir bool) bool {
	return strings.HasPrefix(elem, ".") || dir && elem == "testdata"
}

func dirFiles(root string) ([]corpusFile, error) {
	var files []corpusFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if d.IsDir() {
			if skipped(d.Name(), true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipped(d.Name(), false) {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		files = append(files, corpusFile{rel: filepath.ToSlash(rel), read: func() ([]byte, error) { return os.ReadFile(p) }})
		return nil
	})
	return files, err
}

func tarFiles(name, sub string) ([]corpusFile, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(sub, "/")
	if prefix != "" {
		prefix += "/"
	}
	var files []corpusFile
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		_, rel, ok := strings.Cut(path.Clean(hdr.Name), "/") // strip the archive's top directory
		if !ok || !strings.HasPrefix(rel, prefix) {
			continue
		}
		rel = strings.TrimPrefix(rel, prefix)
		if hidden(rel) || hdr.Size > maxFileBytes {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files = append(files, corpusFile{rel: rel, read: func() ([]byte, error) { return b, nil }})
	}
}

// hidden applies the directory walk's skip rules to a slash path.
func hidden(rel string) bool {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if skipped(p, i < len(parts)-1) {
			return true
		}
	}
	return false
}

func countLines(b []byte) int {
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// readPin returns the pin bench/corpus.sh writes next to the archive
// (<archive>.pin), or into a checkout directory (.pin).
func readPin(src string) string {
	for _, p := range []string{src + ".pin", filepath.Join(src, ".pin")} {
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}
