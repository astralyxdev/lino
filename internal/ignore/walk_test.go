package ignore

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fsNode struct {
	path, content, link string // link: symlink target instead of a file
}

func buildTree(t *testing.T, root string, nodes []fsNode) {
	t.Helper()
	for _, n := range nodes {
		p := filepath.Join(root, filepath.FromSlash(n.path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if n.link != "" {
			if err := os.Symlink(n.link, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if strings.HasSuffix(n.path, "/") {
			continue
		}
		if err := os.WriteFile(p, []byte(n.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func walkRels(t *testing.T, root string) []string {
	t.Helper()
	var got []string
	err := Walk(root, nil, func(e Entry) error {
		got = append(got, e.Rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestWalk(t *testing.T) {
	outside := t.TempDir()
	buildTree(t, outside, []fsNode{{path: "secret.txt", content: "x"}, {path: "d/f.txt", content: "x"}})

	tests := []struct {
		name  string
		nodes []fsNode
		want  []string
	}{
		{
			name: "gitignore excludes",
			nodes: []fsNode{
				{path: ".gitignore", content: "*.log\nbuild/\n"},
				{path: "a.go"}, {path: "x.log"}, {path: "build/out.bin"},
			},
			want: []string{".gitignore", "a.go"},
		},
		{
			name: "linoignore excludes on top",
			nodes: []fsNode{
				{path: ".gitignore", content: "*.log\n"},
				{path: ".linoignore", content: "vendor/\n*.md\n"},
				{path: "a.go"}, {path: "README.md"}, {path: "vendor/v.go"}, {path: "x.log"},
			},
			want: []string{".gitignore", ".linoignore", "a.go"},
		},
		{
			name: "linoignore re-includes file",
			nodes: []fsNode{
				{path: ".gitignore", content: "*.log\n"},
				{path: ".linoignore", content: "!keep.log\n"},
				{path: "keep.log"}, {path: "drop.log"},
			},
			want: []string{".gitignore", ".linoignore", "keep.log"},
		},
		{
			name: "linoignore re-includes dir over nested gitignore",
			nodes: []fsNode{
				{path: "gen/.gitignore", content: "*\n"},
				{path: ".linoignore", content: "!gen/**\n"},
				{path: "gen/a.pb.go"},
			},
			want: []string{".linoignore", "gen/.gitignore", "gen/a.pb.go"},
		},
		{
			name: "nested linoignore",
			nodes: []fsNode{
				{path: "sub/.linoignore", content: "*.tmp\n"},
				{path: "sub/a.tmp"}, {path: "b.tmp"},
			},
			want: []string{"b.tmp", "sub/.linoignore"},
		},
		{
			name: "git info exclude",
			nodes: []fsNode{
				{path: ".git/info/exclude", content: "local.txt\n"},
				{path: ".git/HEAD", content: "ref"},
				{path: "local.txt"}, {path: "a.go"},
			},
			want: []string{"a.go"},
		},
		{
			name: "lino dir never yielded even if re-included",
			nodes: []fsNode{
				{path: ".linoignore", content: "!.lino/\n!.lino/**\n"},
				{path: ".lino/index.db"}, {path: ".lino/config"}, {path: "sub/.lino/x"}, {path: "a.go"},
			},
			want: []string{".linoignore", "a.go", "sub/.lino/x"},
		},
		{
			name: "escaping symlinks skipped",
			nodes: []fsNode{
				{path: "a.go"},
				{path: "out.txt", link: filepath.Join(outside, "secret.txt")},
				{path: "outdir", link: filepath.Join(outside, "d")},
				{path: "rel.txt", link: "../../../../../../../../" + strings.TrimPrefix(filepath.Join(outside, "secret.txt"), "/")},
				{path: "dangling", link: "nope"},
			},
			want: []string{"a.go"},
		},
		{
			name: "in-root file symlink yielded, dir symlink not followed",
			nodes: []fsNode{
				{path: "real/a.go"},
				{path: "alias.go", link: "real/a.go"},
				{path: "realdir", link: "real"},
				{path: "db", link: ".lino/index.db"},
				{path: ".lino/index.db"},
			},
			want: []string{"alias.go", "real/a.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			buildTree(t, root, tt.nodes)
			if got := walkRels(t, root); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWalkEntry(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, []fsNode{{path: "a.txt", content: "hello"}, {path: "l.txt", link: "a.txt"}})
	var got []Entry
	if err := Walk(root, nil, func(e Entry) error { got = append(got, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries", len(got))
	}
	croot, _ := filepath.EvalSymlinks(root)
	for _, e := range got {
		if e.Size != 5 || e.Abs != filepath.Join(croot, "a.txt") || !e.Mode.IsRegular() {
			t.Errorf("bad entry %+v", e)
		}
	}
	if got[0].Link || !got[1].Link {
		t.Errorf("link flags wrong: %+v", got)
	}
}

func TestWalkSkipAll(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, []fsNode{{path: "a"}, {path: "b"}, {path: "c"}})
	n := 0
	err := Walk(root, nil, func(Entry) error { n++; return fs.SkipAll })
	if err != nil || n != 1 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}

func TestRulesIgnored(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, []fsNode{
		{path: ".gitignore", content: "build/\n*.log\n"},
		{path: ".linoignore", content: "!important.log\n"},
		{path: "pkg/.gitignore", content: "gen.go\n"},
		{path: "pkg/.linoignore", content: "secret/\n"},
	})
	r, err := NewRules(root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"a.go", false, false},
		{"x.log", false, true},
		{"important.log", false, false},
		{"build", true, true},
		{"build/a.go", false, true},
		{"pkg/gen.go", false, true},
		{"pkg/secret/k", false, true},
		{"pkg/ok.go", false, false},
		{".lino", true, true},
		{".lino/index.db", false, true},
		{".git/config", false, true},
		{".", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			if got := r.Ignored(tt.rel, tt.isDir); got != tt.want {
				t.Errorf("Ignored(%q) = %v, want %v", tt.rel, got, tt.want)
			}
		})
	}
}
