package live

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/initcmd"
	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/proto"
	"github.com/astralyx/lino/internal/registry"
)

// shortTemp avoids the ~104-byte unix socket path limit.
func shortTemp(t *testing.T, prefix string) string {
	t.Helper()
	d, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	d, err = filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func initRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := shortTemp(t, "lr")
	for p, s := range files {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := initcmd.Init(context.Background(), root, root); err != nil {
		t.Fatal(err)
	}
	return root
}

func call(t *testing.T, sock string, req *proto.Request) *proto.Response {
	t.Helper()
	c, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := proto.WriteRequest(c, req); err != nil {
		t.Fatal(err)
	}
	resp, err := proto.NewReader(c).ReadResponse()
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestStartServeStop(t *testing.T) {
	root := initRoot(t, map[string]string{"a.txt": "one\ntwo\n", "sub/b.txt": "b\n"})
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}
	p, err := Start(context.Background(), Options{Dir: filepath.Join(root, "sub"), Name: "proj", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != root || p.ID != registry.IDFor(root) || p.Startup.Files != 2 {
		t.Fatalf("process %+v", p)
	}
	e, err := reg.Lookup("proj")
	if err != nil || e.ID != p.ID || e.PID != os.Getpid() || registry.Check(e) != registry.Live {
		t.Fatalf("entry %+v, %v", e, err)
	}
	st, err := os.Stat(e.Socket)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v", st, err)
	}

	tests := []struct {
		name    string
		req     *proto.Request
		outcome outcome.Outcome
		stdout  string
	}{
		{"read", &proto.Request{Command: "read", Args: []string{"a.txt"}, Cwd: root}, outcome.OK, "a.txt v="},
		{"read from subdir", &proto.Request{Command: "read", Args: []string{"b.txt"}, Cwd: filepath.Join(root, "sub")}, outcome.OK, "sub/b.txt v="},
		{"read lines flag", &proto.Request{Command: "read", Args: []string{"a.txt"}, Flags: map[string][]string{"lines": {"2"}}, Cwd: root}, outcome.OK, "a.txt v="},
		{"missing", &proto.Request{Command: "read", Args: []string{"nope"}, Cwd: root}, outcome.NotFound, ""},
		{"run refused", &proto.Request{Command: "run"}, outcome.Usage, ""},
		{"unknown", &proto.Request{Command: "frobnicate"}, outcome.Usage, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := call(t, e.Socket, tt.req)
			if resp.Outcome != tt.outcome {
				t.Fatalf("outcome %s, want %s: %s %s", resp.Outcome, tt.outcome, resp.Message, resp.Stderr)
			}
			if len(resp.Stdout) < len(tt.stdout) || resp.Stdout[:len(tt.stdout)] != tt.stdout {
				t.Fatalf("stdout %q", resp.Stdout)
			}
		})
	}

	resp := call(t, e.Socket, &proto.Request{Command: "read", Args: []string{"a.txt"}, Flags: map[string][]string{"lines": {"2"}}, Cwd: root, JSON: true})
	var d struct{ Lines []string }
	if err := json.Unmarshal(resp.Data, &d); err != nil || len(d.Lines) != 1 || d.Lines[0] != "two" {
		t.Fatalf("json data %s: %v", resp.Data, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(ctx); err != nil {
		t.Fatal("second stop:", err)
	}
	if _, err := reg.Read(p.ID); !outcome.Is(err, outcome.NotFound) {
		t.Fatalf("entry not removed: %v", err)
	}
	if _, err := os.Stat(e.Socket); !os.IsNotExist(err) {
		t.Fatalf("socket not removed: %v", err)
	}
}

func TestStartErrors(t *testing.T) {
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}
	plain := shortTemp(t, "lp")
	root := initRoot(t, map[string]string{"a.txt": "a\n"})
	p, err := Start(context.Background(), Options{Dir: root, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background()) })

	tests := []struct {
		name string
		opt  Options
		want outcome.Outcome
	}{
		{"not initialised", Options{Dir: plain, Registry: reg}, outcome.NotRunning},
		{"missing dir", Options{Dir: filepath.Join(plain, "nope"), Registry: reg}, outcome.NotFound},
		{"bad name", Options{Dir: root, Name: "a b", Registry: reg}, outcome.Usage},
		{"second process", Options{Dir: root, Registry: reg}, outcome.LiveExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Start(context.Background(), tt.opt)
			if got := outcome.Of(err); got != tt.want {
				t.Fatalf("outcome %s (%v), want %s", got, err, tt.want)
			}
		})
	}
}

func TestStoredName(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"", ""},
		{"demo\n", "demo"},
		{"  demo  ", "demo"},
		{"bad name\n", ""},
	} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, ".lino"), 0o755); err != nil {
			t.Fatal(err)
		}
		if tc.file != "" {
			if err := os.WriteFile(namePath(root), []byte(tc.file), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := StoredName(root); got != tc.want {
			t.Errorf("file %q: StoredName = %q, want %q", tc.file, got, tc.want)
		}
	}
}

func TestStoredNameTakenFallsBack(t *testing.T) {
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}
	a := initRoot(t, map[string]string{"a.txt": "a\n"})
	b := initRoot(t, map[string]string{"b.txt": "b\n"})
	pa, err := Start(context.Background(), Options{Dir: a, Name: "demo", Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pa.Stop(context.Background()) })
	if got := StoredName(a); got != "demo" {
		t.Fatalf("StoredName = %q, want demo", got)
	}
	if err := saveName(b, "demo"); err != nil {
		t.Fatal(err)
	}
	pb, err := Start(context.Background(), Options{Dir: b, Registry: reg})
	if err != nil {
		t.Fatalf("start with taken stored name: %v", err)
	}
	t.Cleanup(func() { pb.Stop(context.Background()) })
	if pb.Name != "" {
		t.Errorf("name %q, want none", pb.Name)
	}
	if got := StoredName(b); got != "demo" {
		t.Errorf("stored name changed to %q", got)
	}
}
