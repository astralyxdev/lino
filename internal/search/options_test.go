package search

import (
	"reflect"
	"testing"
)

func TestMatchPath(t *testing.T) {
	tests := []struct {
		globs []string
		path  string
		want  bool
	}{
		{nil, "a/b.go", true},
		{[]string{"wallet"}, "wallet/a.go", true},
		{[]string{"wallet"}, "walletx/a.go", false},
		{[]string{"wallet/**"}, "wallet/x/a.go", true},
		{[]string{"wallet/*"}, "wallet/a.go", true},
		{[]string{"*.go"}, "deep/dir/a.go", true},
		{[]string{"*.go"}, "a.md", false},
		{[]string{"a/**/c.go"}, "a/c.go", true},
		{[]string{"a/**/c.go"}, "a/b/b/c.go", true},
		{[]string{"x/*.go"}, "a/x/b.go", false},
		{[]string{"nope", "*.md"}, "docs/n.md", true},
		{[]string{"**"}, "any/thing", true},
	}
	for _, tt := range tests {
		if got := MatchPath(tt.globs, tt.path); got != tt.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", tt.globs, tt.path, got, tt.want)
		}
	}
}

func TestNormalizeGlobs(t *testing.T) {
	tests := []struct {
		globs []string
		dir   string
		want  []string
	}{
		{[]string{"wallet/", "./a/*.go", "."}, ".", []string{"wallet", "a/*.go", "**"}},
		{[]string{"*.go", "x/y.go", "/root.go", "."}, "wallet", []string{"wallet/**/*.go", "wallet/x/y.go", "root.go", "wallet"}},
	}
	for _, tt := range tests {
		if got := NormalizeGlobs(tt.globs, tt.dir); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("NormalizeGlobs(%q, %q) = %q, want %q", tt.globs, tt.dir, got, tt.want)
		}
	}
}
