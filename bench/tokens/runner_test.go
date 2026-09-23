package tokens

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		cmd                   string
		lino, fallback, other int
	}{
		{"lino read src/a.go --lines 1:20", 1, 0, 0},
		{"grep -rn foo . | head -20", 0, 2, 0},
		{"cd net/http && rg -n Foo", 0, 1, 1},
		{"lino edit a.go 3 4 --v abc <<'EOF'\ncat this is body\ngrep too\nEOF", 1, 0, 0},
		{"cat > new.go <<EOF\npackage x\nEOF", 0, 1, 0},
		{"lino edit a.go 1 1 --v x <<< 'sed'", 1, 0, 0},
		{"FOO=1 /usr/bin/sed -i '' s/a/b/ f", 0, 1, 0},
		{"for f in $(find . -name '*.go'); do wc -l $f; done", 0, 2, 0},
		{"go vet ./...; echo done", 0, 0, 2},
		{"", 0, 0, 0},
	}
	for _, tt := range tests {
		got := Classify(tt.cmd)
		if got.Lino != tt.lino || got.Fallback != tt.fallback || got.Other != tt.other {
			t.Errorf("Classify(%q) = %+v, want lino %d fallback %d other %d", tt.cmd, got, tt.lino, tt.fallback, tt.other)
		}
	}
}

const stream = `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"text","text":"looking"},{"type":"tool_use","id":"a","name":"Bash","input":{"command":"lino search Foo"}},{"type":"tool_use","id":"b","name":"Bash","input":{"command":"cat go.mod"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"src/a.go:3: Foo"},{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"module std"}]}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c","name":"Bash","input":{"command":"lino edit a.go 3 3 --v 1 <<'EOF'\nx\nEOF"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c","content":"conflict: a.go changed since v=1\nhint: ..."}]}}
not json
{"type":"result","subtype":"success","is_error":false,"num_turns":4,"total_cost_usd":0.12,"result":"done","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":1000,"cache_read_input_tokens":3000}}
`

func TestParseStream(t *testing.T) {
	tr, err := ParseStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	want := Ops{Lino: 2, Fallback: 1, Commands: 3, Retries: 1}
	if tr.Ops != want || tr.Usage.Total() != 4150 || tr.Turns != 4 || tr.CostUSD != 0.12 || len(tr.Commands) != 3 {
		t.Fatalf("%+v", tr)
	}
	if r := tr.Ops.FallbackRate(); r < 0.33 || r > 0.34 {
		t.Errorf("fallback rate %v", r)
	}
}

func TestReport(t *testing.T) {
	tr, _ := ParseStream(strings.NewReader(stream))
	r := &Report{Agent: "test", Pin: Pin, Arms: []string{"grep", "lino"}, Results: []RunResult{
		{Task: "T01", Arm: "grep", OK: true, Transcript: Transcript{Usage: Usage{Input: 10}}},
		{Task: "T01", Arm: "lino", OK: false, Transcript: tr},
		{Task: "T01", Arm: "lino", Rep: 1, OK: true, Transcript: tr, Error: "agent: boom"},
	}}
	var b bytes.Buffer
	if err := r.Markdown(&b); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"| T01 Raise net/http's default header size limit | 10 | 1/1 | 4150 | 1/2 | 33% | 2 |",
		"| lino | 2 | 1 | 8300 | 100 | 6 | 4 | 2 | 33.3% | 2 | 0.24 |",
		"- T01 lino #1: agent: boom",
	} {
		if !strings.Contains(b.String(), s) {
			t.Errorf("report lacks %q\n%s", s, b.String())
		}
	}
	if err := r.Write(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// TestRunOne runs T17 in every arm with a shell-script agent.
func TestRunOne(t *testing.T) {
	if testing.Short() {
		t.Skip("copies the corpus and builds lino")
	}
	src := corpus(t)
	work := t.TempDir()
	lino := filepath.Join(work, "lino")
	c := exec.Command("go", "build", "-o", lino, "github.com/astralyx/lino/cmd/lino")
	c.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	script := `echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Bash","input":{"command":"sed -i x go.mod"}}]}}'
sed 's/crypto v0.30.0/crypto v0.31.0/' go.mod > go.mod.new && mv go.mod.new go.mod
sed 's/crypto v0.30.0/crypto v0.31.0/' vendor/modules.txt > m.new && mv m.new vendor/modules.txt
command -v rg >/dev/null 2>&1 && rg --version >/dev/null 2>&1 && echo rg-works >&2
[ "$LINO_BENCH_ARM" = lino ] && lino status >/dev/null 2>&1 || true
echo '{"type":"result","num_turns":1,"usage":{"input_tokens":7,"output_tokens":3}}'`
	task, _ := Lookup("T17")
	for _, arm := range Arms {
		cfg := RunConfig{Corpus: src, Work: work, Lino: lino, Timeout: time.Minute,
			Agent: func(system, prompt string) []string {
				if arm.Lino != strings.Contains(system, "# lino agent instructions") {
					t.Errorf("%s: help block in system prompt: %v", arm.Name, !arm.Lino)
				}
				return []string{"sh", "-c", script}
			}}
		res := RunOne(context.Background(), cfg, task, arm, 0)
		if !res.OK || res.Error != "" || res.Transcript.Usage.Total() != 10 || res.Transcript.Ops.Fallback != 1 {
			t.Errorf("%s: %+v", arm.Name, res)
		}
		stderr, _ := os.ReadFile(filepath.Join(work, "T17-"+arm.Name+"-0", "agent.stderr"))
		if bytes.Contains(stderr, []byte("rg-works")) {
			t.Errorf("%s: rg not denied", arm.Name)
		}
	}
}
