package live

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
	"github.com/astralyx/lino/internal/registry"
)

func TestRunNamesRunningProcess(t *testing.T) {
	ctx := context.Background()
	reg := &registry.Registry{Dir: filepath.Join(shortTemp(t, "lh"), "run")}
	a := initRoot(t, map[string]string{"a.txt": "a\n"})
	b := initRoot(t, map[string]string{"b.txt": "b\n"})
	for _, root := range []string{a, b} {
		p, err := Start(ctx, Options{Dir: root, Registry: reg})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Stop(context.Background()) })
	}

	tests := []struct {
		name, dir, alias string
		want             outcome.Outcome
		wantName         string // entry name of dir afterwards
		wantMsg          string
	}{
		{"plain run", a, "", outcome.OK, "", "already running"},
		{"names a", a, "demo", outcome.OK, "demo", "already running (named demo)"},
		{"same name again", a, "demo", outcome.OK, "demo", "already running (named demo)"},
		{"held by a", b, "demo", outcome.Refused, "", ""},
		{"invalid", b, "-x", outcome.Usage, "", ""},
		{"renames a", a, "demo2", outcome.OK, "demo2", "already running (named demo2)"},
		{"freed name", b, "demo", outcome.OK, "demo", "already running (named demo)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(ctx, RunRequest{Dir: tc.dir, Name: tc.alias, Registry: reg})
			if got := outcome.Of(err); got != tc.want {
				t.Fatalf("outcome %s (%v), want %s", got, err, tc.want)
			}
			e, rerr := reg.Read(registry.IDFor(tc.dir))
			if rerr != nil || e.Name != tc.wantName {
				t.Fatalf("entry %+v %v, want name %q", e, rerr, tc.wantName)
			}
			if err != nil {
				return
			}
			if res.Message != tc.wantMsg {
				t.Errorf("message %q, want %q", res.Message, tc.wantMsg)
			}
			if d := res.Data.(RunData); !d.Existing || d.Name != tc.wantName {
				t.Errorf("data %+v", d)
			}
			if tc.alias != "" && StoredName(tc.dir) != tc.alias {
				t.Errorf("stored name %q, want %q", StoredName(tc.dir), tc.alias)
			}
		})
	}
	if _, err := reg.Lookup("demo2"); err != nil {
		t.Errorf("lookup demo2: %v", err)
	}
}
