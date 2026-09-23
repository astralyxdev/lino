package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureLinoignore(t *testing.T) {
	tests := []struct {
		name    string
		prior   *string
		created bool
	}{
		{"missing", nil, true},
		{"empty kept", strp(""), false},
		{"custom kept", strp("*.log\n"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			p := filepath.Join(root, LinoignoreFile)
			want := DefaultLinoignoreContent()
			if tt.prior != nil {
				want = []byte(*tt.prior)
				if err := os.WriteFile(p, want, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			created, err := EnsureLinoignore(root)
			if err != nil || created != tt.created {
				t.Fatalf("EnsureLinoignore = %v, %v; want %v", created, err, tt.created)
			}
			if again, err := EnsureLinoignore(root); err != nil || again {
				t.Fatalf("second call = %v, %v", again, err)
			}
			got, err := os.ReadFile(p)
			if err != nil || string(got) != string(want) {
				t.Fatalf("content %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestDefaultLinoignoreRules(t *testing.T) {
	root := t.TempDir()
	if _, err := EnsureLinoignore(root); err != nil {
		t.Fatal(err)
	}
	r, err := NewRules(root)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		rel  string
		dir  bool
		want bool
	}{
		{"node_modules", true, true},
		{"web/node_modules", true, true},
		{"app.min.js", false, true},
		{"sub/.DS_Store", false, true},
		{"src/main.go", false, false},
		{".linoignore", false, false},
	}
	for _, tt := range tests {
		if got := r.Ignored(tt.rel, tt.dir); got != tt.want {
			t.Errorf("Ignored(%q) = %v, want %v", tt.rel, got, tt.want)
		}
	}
}

func strp(s string) *string { return &s }
