package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const bom = "\xef\xbb\xbf"

// filesRoot is the shared starting tree for the file-command cases.
func filesRoot(t *testing.T) *Harness {
	h := NewInit(t).Fixture("wallet").
		Write("wallet/service.go", serviceGo()).
		Write("crlf.txt", bom+"one\r\ntwo\r\nthree").
		Write("dup.txt", "a\n}\nb\n}\n").
		Write("bin.dat", "ab\x00cd").
		Mkdir("dir")
	if err := os.Chmod(h.Path("crlf.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

// ver returns the current version of rel via read --json.
func (h *Harness) ver(rel string) string {
	h.t.Helper()
	r := h.Run("read", rel, "--json", "--direct")
	var env struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &env); err != nil || env.Data.Version == "" {
		h.t.Fatalf("read %s: %v\n%s", rel, err, h.Transcript(r))
	}
	return env.Data.Version
}

// vFor substitutes "$V" in args with the version of file.
func vFor(h *Harness, file string, args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.Contains(a, "$V") {
			a = strings.ReplaceAll(a, "$V", h.ver(file))
		}
		out[i] = a
	}
	return out
}

func TestFileCommands(t *testing.T) {
	tests := []struct {
		name  string
		file  string // file whose version replaces $V
		args  []string
		stdin string
		exit  int
		check map[string]string // expected file contents after; "" = must not exist
	}{
		// edit
		{"edit_anchored", "wallet/service.go", []string{"edit", "wallet/service.go", "13:0nd", "15", "--v", "$V"},
			"    if amt <= 0 || amt > MaxWithdraw {\n        return ErrInvalidAmount\n    }\n", 0, nil},
		{"edit_crlf_bom", "crlf.txt", []string{"edit", "crlf.txt", "2", "2", "--v", "$V"}, "TWO\nextra\n", 0,
			map[string]string{"crlf.txt": bom + "one\r\nTWO\r\nextra\r\nthree"}},
		{"edit_empty_stdin", "crlf.txt", []string{"edit", "crlf.txt", "1", "1", "--v", "$V"}, "", 2, nil},
		{"edit_stale_anchor", "wallet/service.go", []string{"edit", "wallet/service.go", "13:zzz", "13", "--v", "$V"}, "x\n", 4, nil},
		{"edit_stale_v", "", []string{"edit", "wallet/service.go", "13", "13", "--v", "000000"}, "x\n", 6, nil},
		{"edit_missing_v", "", []string{"edit", "wallet/service.go", "13", "13"}, "x\n", 6, nil},
		{"edit_bad_anchor", "", []string{"edit", "wallet/service.go", "x:1", "13", "--v", "000000"}, "x\n", 2, nil},
		{"edit_binary", "", []string{"edit", "bin.dat", "1", "1", "--v", "000000"}, "x\n", 7, nil},
		{"edit_outside", "", []string{"edit", "../x", "1", "1", "--v", "000000"}, "x\n", 7, nil},
		{"edit_missing_file", "", []string{"edit", "nope.txt", "1", "1", "--v", "000000"}, "x\n", 3, nil},
		// insert
		{"insert_after", "crlf.txt", []string{"insert", "crlf.txt", "--after", "1", "--v", "$V"}, "new\n", 0,
			map[string]string{"crlf.txt": bom + "one\r\nnew\r\ntwo\r\nthree"}},
		{"insert_at_end", "crlf.txt", []string{"insert", "crlf.txt", "--at-end", "--v", "$V"}, "four\n", 0,
			map[string]string{"crlf.txt": bom + "one\r\ntwo\r\nthree\r\nfour"}},
		{"insert_two_positions", "crlf.txt", []string{"insert", "crlf.txt", "--at-start", "--at-end", "--v", "$V"}, "x\n", 2, nil},
		// delete
		{"delete_range", "wallet/service.go", []string{"delete", "wallet/service.go", "13:0nd", "15", "--v", "$V"}, "", 0, nil},
		{"delete_last_crlf", "crlf.txt", []string{"delete", "crlf.txt", "3", "3", "--v", "$V"}, "", 0,
			map[string]string{"crlf.txt": bom + "one\r\ntwo"}},
		{"delete_beyond_eof", "crlf.txt", []string{"delete", "crlf.txt", "2", "9", "--v", "$V"}, "", 4, nil},
		// replace
		{"replace_unique", "wallet/service.go", []string{"replace", "wallet/service.go", "--v", "$V"},
			"        return ErrInvalidAmount\n<<<lino>>>\n        return fmt.Errorf(\"amount %d: %w\", amt, ErrInvalidAmount)\n", 0, nil},
		{"replace_ambiguous", "dup.txt", []string{"replace", "dup.txt", "--v", "$V"}, "}\n<<<lino>>>\n};\n", 5, nil},
		{"replace_not_found", "dup.txt", []string{"replace", "dup.txt", "--v", "$V"}, "zzz\n<<<lino>>>\ny\n", 3, nil},
		{"replace_no_sep", "dup.txt", []string{"replace", "dup.txt", "--v", "$V"}, "a\n", 2, nil},
		// write
		{"write_create", "", []string{"write", "new/dir/f.txt"}, "hello\nworld", 0,
			map[string]string{"new/dir/f.txt": "hello\nworld\n"}},
		{"write_overwrite_no_v", "", []string{"write", "dup.txt"}, "x\n", 6, nil},
		{"write_overwrite_crlf", "crlf.txt", []string{"write", "crlf.txt", "--v", "$V"}, "x\ny\n", 0,
			map[string]string{"crlf.txt": bom + "x\r\ny"}},
		{"write_force", "", []string{"write", "dup.txt", "--force"}, "forced\n", 0,
			map[string]string{"dup.txt": "forced\n"}},
		{"write_into_meta", "", []string{"write", ".lino/x"}, "x\n", 7, nil},
		// mv
		{"mv_rename", "", []string{"mv", "dup.txt", "moved/dup.txt"}, "", 0,
			map[string]string{"dup.txt": "", "moved/dup.txt": "a\n}\nb\n}\n"}},
		{"mv_binary", "", []string{"mv", "bin.dat", "bin2.dat"}, "", 0,
			map[string]string{"bin.dat": "", "bin2.dat": "ab\x00cd"}},
		{"mv_target_exists", "", []string{"mv", "dup.txt", "crlf.txt"}, "", 6, nil},
		{"mv_missing", "", []string{"mv", "nope.txt", "x.txt"}, "", 3, nil},
		{"mv_escape", "", []string{"mv", "dup.txt", "../out.txt"}, "", 7, nil},
		// rm
		{"rm_v", "dup.txt", []string{"rm", "dup.txt", "--v", "$V"}, "", 0, map[string]string{"dup.txt": ""}},
		{"rm_no_v", "", []string{"rm", "dup.txt"}, "", 6, nil},
		{"rm_stale_v", "", []string{"rm", "dup.txt", "--v", "000000"}, "", 6, nil},
		{"rm_binary_needs_force", "", []string{"rm", "bin.dat", "--v", "000000"}, "", 7, nil},
		{"rm_binary_force", "", []string{"rm", "bin.dat", "--force"}, "", 0, map[string]string{"bin.dat": ""}},
		{"rm_missing", "", []string{"rm", "nope.txt", "--force"}, "", 3, nil},
		// usage
		{"unknown_command", "", []string{"frobnicate"}, "", 2, nil},
		{"unknown_flag", "", []string{"read", "dup.txt", "--bogus"}, "", 2, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := filesRoot(t)
			before := snapshot(h)
			args := tt.args
			if tt.file != "" {
				args = vFor(h, tt.file, args)
			}
			if args[0] != "frobnicate" {
				args = append(args, "--direct")
			}
			r := h.Exec(Cmd{Args: args, Stdin: tt.stdin})
			h.ExpectExit(r, tt.exit)
			h.Golden("files_"+tt.name, r)
			after := snapshot(h)
			if tt.exit != 0 {
				if !equalMaps(before, after) {
					t.Errorf("failed command changed the tree:\nbefore %q\nafter  %q", before, after)
				}
				if r.Stderr == "" {
					t.Error("failure without a message on stderr")
				}
			}
			for p, want := range tt.check {
				got, ok := after[p]
				switch {
				case want == "" && ok:
					t.Errorf("%s still exists", p)
				case want != "" && got != want:
					t.Errorf("%s = %q, want %q", p, got, want)
				}
			}
		})
	}
}

func snapshot(h *Harness) map[string]string {
	m := map[string]string{}
	for _, p := range h.Tree() {
		m[p] = h.Read(p)
	}
	return m
}

func equalMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// TestEditWorkflow is the loop an agent runs: read with anchors, edit with
// the printed anchors and version, and see the stale version rejected.
func TestEditWorkflow(t *testing.T) {
	h := filesRoot(t)
	if err := os.Chmod(h.Path("wallet/service.go"), 0o754); err != nil {
		t.Fatal(err)
	}
	r := h.Run("read", "wallet/service.go", "--lines", "13:15", "--anchors", "--direct")
	h.ExpectExit(r, 0)
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	var v string
	for _, f := range strings.Fields(lines[0]) {
		if strings.HasPrefix(f, "v=") {
			v = strings.TrimPrefix(f, "v=")
		}
	}
	start := strings.SplitN(lines[1], "|", 2)[0]
	end := strings.SplitN(lines[3], "|", 2)[0]

	r = h.Exec(Cmd{Args: []string{"edit", "wallet/service.go", start, end, "--v", v, "--direct"}, Stdin: "    if amt <= 0 || amt > MaxWithdraw {\n"})
	h.ExpectExit(r, 0)
	if !strings.HasPrefix(r.Stdout, "updated wallet/service.go v="+v+"→") || r.Stderr != "" {
		t.Errorf("edit output: %s", h.Transcript(r))
	}
	got := strings.Split(h.Read("wallet/service.go"), "\n")
	if len(got) != 239 || got[12] != "    if amt <= 0 || amt > MaxWithdraw {" || got[13] != "// 16" {
		t.Errorf("unexpected content around the edit: %q", got[10:15])
	}
	st, _ := os.Stat(h.Path("wallet/service.go"))
	if st.Mode().Perm() != 0o754 {
		t.Errorf("mode = %v, want 0754", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Join(h.Root, "wallet"))
	if len(entries) != 2 {
		t.Errorf("temp files left behind: %v", entries)
	}

	// The same version again: the edited region changed, so it conflicts.
	r = h.Exec(Cmd{Args: []string{"edit", "wallet/service.go", "13", "13", "--v", v, "--direct"}, Stdin: "x\n"})
	h.ExpectExit(r, 6)
	if !strings.Contains(r.Stdout+r.Stderr, "MaxWithdraw") {
		t.Errorf("conflict does not show current lines: %s", h.Transcript(r))
	}
}

// TestJSONSchema snapshots the shape (keys and types) of every file
// command's --json envelope, success and failure. A change here is a change
// to the versioned --json contract.
func TestJSONSchema(t *testing.T) {
	cases := []struct {
		name  string
		file  string
		args  []string
		stdin string
	}{
		{"read", "", []string{"read", "dup.txt"}, ""},
		{"read_anchors_truncated", "", []string{"read", "big.txt", "--anchors"}, ""},
		{"edit", "crlf.txt", []string{"edit", "crlf.txt", "1", "1", "--v", "$V"}, "x\n"},
		{"insert", "crlf.txt", []string{"insert", "crlf.txt", "--at-start", "--v", "$V"}, "x\n"},
		{"delete", "crlf.txt", []string{"delete", "crlf.txt", "1", "1", "--v", "$V"}, ""},
		{"replace", "dup.txt", []string{"replace", "dup.txt", "--v", "$V"}, "a\n<<<lino>>>\nA\n"},
		{"write_create", "", []string{"write", "n.txt"}, "x\n"},
		{"write_update", "", []string{"write", "dup.txt", "--force"}, "x\n"},
		{"mv", "", []string{"mv", "dup.txt", "d2.txt"}, ""},
		{"rm", "", []string{"rm", "dup.txt", "--force"}, ""},
		{"err_usage", "", []string{"read", "dup.txt", "--lines", "0"}, ""},
		{"err_not_found", "", []string{"read", "nope.txt"}, ""},
		{"err_anchor_mismatch", "crlf.txt", []string{"delete", "crlf.txt", "1:zzz", "1", "--v", "$V"}, ""},
		{"err_ambiguous", "dup.txt", []string{"replace", "dup.txt", "--v", "$V"}, "}\n<<<lino>>>\n)\n"},
		{"err_conflict", "", []string{"delete", "crlf.txt", "1", "1", "--v", "000000"}, ""},
		{"err_refused", "", []string{"read", "bin.dat"}, ""},
	}
	var b strings.Builder
	for _, c := range cases {
		h := filesRoot(t).Write("big.txt", numbered(600))
		args := c.args
		if c.file != "" {
			args = vFor(h, c.file, args)
		}
		r := h.Exec(Cmd{Args: append(args, "--json", "--direct"), Stdin: c.stdin})
		var v any
		if err := json.Unmarshal([]byte(r.Stdout), &v); err != nil {
			t.Fatalf("%s: stdout is not one JSON value: %v\n%s", c.name, err, h.Transcript(r))
		}
		fmt.Fprintf(&b, "# %s (exit %d)\n", c.name, r.Exit)
		for _, l := range shape("", v) {
			b.WriteString(l + "\n")
		}
	}
	p := filepath.Join("testdata", "golden", "json_schema.golden")
	if *update {
		if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if b.String() != string(want) {
		t.Errorf("--json schema changed (run with -update to accept)\n--- got\n%s--- want\n%s", b.String(), want)
	}
}

// shape lists "path: type" for every leaf of v; arrays show their first element.
func shape(prefix string, v any) []string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var out []string
		for _, k := range keys {
			out = append(out, shape(prefix+"."+k, x[k])...)
		}
		return out
	case []any:
		if len(x) == 0 {
			return []string{prefix + "[]: empty"}
		}
		return shape(prefix+"[]", x[0])
	case string:
		return []string{prefix + ": string"}
	case float64:
		return []string{prefix + ": number"}
	case bool:
		return []string{prefix + ": bool"}
	case nil:
		return []string{prefix + ": null"}
	}
	return []string{prefix + ": ?"}
}
