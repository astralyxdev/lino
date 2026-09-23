package history

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/linediff"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".lino"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

func TestEncodeLinesRoundTrip(t *testing.T) {
	long := make([]string, 50)
	for i := range long {
		long[i] = fmt.Sprintf("\tif amt <= 0 || amt > MaxWithdraw { // line %d", i)
	}
	tests := []struct {
		name  string
		lines []string
	}{
		{"nil", nil},
		{"one empty line", []string{""}},
		{"two empty lines", []string{"", ""}},
		{"short", []string{"a", "b"}},
		{"unicode", []string{"héllo wörld ✓", "\tкод"}},
		{"trailing empty", []string{"x", ""}},
		{"long compressed", long},
		{"random-ish", []string{"q8#Z!0pL@", "v7&Kw$1mN^rT*y", "e"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := EncodeLines(tt.lines)
			got, err := DecodeLines(b, len(tt.lines))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.lines) || (len(got) > 0 && !reflect.DeepEqual(got, tt.lines)) {
				t.Fatalf("got %q, want %q", got, tt.lines)
			}
		})
	}
}

func TestDecodeLinesCorrupt(t *testing.T) {
	tests := []struct {
		name string
		b    []byte
		n    int
	}{
		{"payload for zero lines", []byte{tagRaw, 'x'}, 0},
		{"empty for one line", nil, 1},
		{"bad tag", []byte{9, 'x'}, 1},
		{"line count mismatch", EncodeLines([]string{"a", "b"}), 3},
		{"bad flate", []byte{tagFlate, 0xff, 0xff}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeLines(tt.b, tt.n); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestAddGetRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	tm := time.Unix(1_790_000_000, 123456789)
	tests := []struct {
		name string
		c    Change
	}{
		{"edit", Change{ID: 1047, Time: tm, Source: SourceLino, Author: "agent-2", Path: "wallet/service.go", Op: OpEdit,
			VBefore: "8c21e0", VAfter: "5f02aa",
			Fragments: []linediff.Fragment{{Pos: 12, Old: []string{"    if amt <= 0 {"}, New: []string{"    if amt <= 0 || amt > MaxWithdraw {"}}}}},
		{"insert: empty old", Change{ID: 1048, Time: tm, Source: SourceLino, Path: "a.go", Op: OpInsert,
			Fragments: []linediff.Fragment{{Pos: 3, New: []string{"x", ""}}}}},
		{"delete: empty new", Change{ID: 1049, Time: tm, Source: SourceLino, Path: "a.go", Op: OpDelete,
			Fragments: []linediff.Fragment{{Pos: 0, Old: []string{"", "y"}}}}},
		{"multi fragment external", Change{ID: 1050, Time: tm, Source: SourceExternal, Path: "b.go", Op: OpExternal,
			Fragments: []linediff.Fragment{{Pos: 1, Old: []string{"a"}, New: []string{"b"}}, {Pos: 9, Old: []string{"c", "d"}}}}},
		{"mv no fragments", Change{ID: 1051, Time: tm, Source: SourceLino, Path: "new/x.go", Op: OpMv, Extra: Extra{From: "old/x.go"}}},
		{"rm with mode", Change{ID: 1052, Time: tm, Source: SourceLino, Path: "gone.sh", Op: OpRm, Extra: Extra{Removed: true, Mode: 0o755},
			Fragments: []linediff.Fragment{{Pos: 0, Old: []string{"#!/bin/sh", "echo hi"}}}}},
		{"rollback", Change{ID: 1053, Time: tm, Source: SourceLino, Path: "a.go", Op: OpRollback, Extra: Extra{Undid: 1047}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := s.Add(ctx, tt.c)
			if err != nil {
				t.Fatal(err)
			}
			if id != tt.c.ID {
				t.Fatalf("id %d, want %d", id, tt.c.ID)
			}
			got, err := s.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Time.Equal(tt.c.Time) {
				t.Fatalf("time %v, want %v", got.Time, tt.c.Time)
			}
			got.Time = tt.c.Time
			if !reflect.DeepEqual(got, tt.c) {
				t.Fatalf("got  %+v\nwant %+v", got, tt.c)
			}
		})
	}
}

func TestAddAssignsIDAndTime(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	a, err := s.Add(ctx, Change{Source: SourceLino, Path: "a", Op: OpWrite, Extra: Extra{Created: true}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Add(ctx, Change{Source: SourceLino, Path: "a", Op: OpEdit})
	if err != nil {
		t.Fatal(err)
	}
	if b != a+1 {
		t.Fatalf("ids %d, %d", a, b)
	}
	c, err := s.Get(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(c.Time) > time.Minute || !c.Extra.Created {
		t.Fatalf("got %+v", c)
	}
	if _, err := s.Add(ctx, Change{ID: a, Source: SourceLino, Path: "a", Op: OpEdit}); err == nil {
		t.Fatal("duplicate id accepted")
	}
	if latest, _ := s.Latest(ctx); latest != b {
		t.Fatalf("latest %d, want %d", latest, b)
	}
}

func TestGetNotFound(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.Get(context.Background(), 42); err != ErrNotFound {
		t.Fatalf("err %v", err)
	}
}

func TestList(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for _, c := range []Change{
		{ID: 1, Path: "wallet/a.go", Op: OpEdit, Author: "agent-1"},
		{ID: 2, Path: "wallet/b.go", Op: OpWrite, Author: "agent-2"},
		{ID: 3, Path: "api/h.go", Op: OpEdit, Author: "agent-1"},
		{ID: 4, Path: "api/c.go", Op: OpMv, Extra: Extra{From: "wallet/c.go"}},
		{ID: 5, Path: "walletx.go", Op: OpEdit},
	} {
		c.Source = SourceLino
		if _, err := s.Add(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		f    Filter
		want []int64
	}{
		{"all newest first", Filter{}, []int64{5, 4, 3, 2, 1}},
		{"asc", Filter{Asc: true}, []int64{1, 2, 3, 4, 5}},
		{"limit", Filter{Limit: 2}, []int64{5, 4}},
		{"dir prefix incl mv source", Filter{Path: "wallet/"}, []int64{4, 2, 1}},
		{"exact path", Filter{Path: "api/h.go"}, []int64{3}},
		{"exact mv source", Filter{Path: "wallet/c.go"}, []int64{4}},
		{"since", Filter{Since: 3}, []int64{5, 4}},
		{"before", Filter{Before: 3}, []int64{2, 1}},
		{"by", Filter{By: "agent-1"}, []int64{3, 1}},
		{"none", Filter{Path: "nope/"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs, err := s.List(ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			var ids []int64
			for _, c := range cs {
				ids = append(ids, c.ID)
			}
			if !reflect.DeepEqual(ids, tt.want) {
				t.Fatalf("ids %v, want %v", ids, tt.want)
			}
		})
	}
}

func TestOpenRecreates(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		prepare   func(t *testing.T, root string)
		wantReset bool
		wantKept  bool
	}{
		{"missing", func(t *testing.T, root string) { os.Remove(Path(root)) }, false, false},
		{"reopen keeps data", func(t *testing.T, root string) {}, false, true},
		{"garbage file", func(t *testing.T, root string) {
			os.Remove(Path(root) + "-wal")
			os.Remove(Path(root) + "-shm")
			os.WriteFile(Path(root), []byte(strings.Repeat("not a database ", 100)), 0o644)
		}, true, false},
		{"newer schema", func(t *testing.T, root string) {
			s, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			s.SQL.Exec(`UPDATE meta SET value = '99' WHERE key = 'schema_version'`)
			s.Close()
		}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, root := newStore(t)
			if _, err := s.Add(ctx, Change{ID: 7, Source: SourceLino, Path: "a", Op: OpEdit}); err != nil {
				t.Fatal(err)
			}
			s.Close()
			tt.prepare(t, root)
			s2, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			if s2.Reset != tt.wantReset {
				t.Fatalf("Reset %v, want %v", s2.Reset, tt.wantReset)
			}
			_, err = s2.Get(ctx, 7)
			if kept := err == nil; kept != tt.wantKept {
				t.Fatalf("kept %v (err %v), want %v", kept, err, tt.wantKept)
			}
			if _, err := s2.Add(ctx, Change{ID: 8, Source: SourceLino, Path: "a", Op: OpEdit}); err != nil {
				t.Fatalf("store unusable: %v", err)
			}
		})
	}
}

func TestOpenMissingDir(t *testing.T) {
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("want error without .lino")
	}
}

// A 20-line edit in a 5,000-line file stores those 20 lines twice, compressed,
// never the file.
func TestFragmentSize(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	before := make([]string, 5000)
	for i := range before {
		before[i] = fmt.Sprintf("\tresult%d := compute(ctx, input%d, opts) // step %d of the pipeline", i, i, i)
	}
	after := append([]string(nil), before...)
	for i := 125; i < 145; i++ {
		after[i] = strings.Replace(after[i], "compute(", "computeChecked(", 1)
	}
	frags := linediff.Diff(before, after)
	if len(frags) != 1 || len(frags[0].Old) != 20 || len(frags[0].New) != 20 {
		t.Fatalf("diff: %d fragments", len(frags))
	}
	if _, err := s.Add(ctx, Change{ID: 1, Source: SourceLino, Path: "big.go", Op: OpEdit, Fragments: frags}); err != nil {
		t.Fatal(err)
	}

	var stored, raw int
	if err := s.SQL.QueryRow(`SELECT sum(length(old_lines) + length(new_lines)) FROM fragments WHERE change_id = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, l := range append(frags[0].Old, frags[0].New...) {
		raw += len(l) + 1
	}
	fileSize := len(strings.Join(before, "\n"))
	t.Logf("20-line edit: raw %d bytes, stored %d bytes, file %d bytes", raw, stored, fileSize)
	if stored >= raw {
		t.Fatalf("stored %d bytes, not compressed below raw %d", stored, raw)
	}
	if stored*20 > fileSize {
		t.Fatalf("stored %d bytes for a %d-byte file: looks like the file, not the fragment", stored, fileSize)
	}
	got, err := s.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := linediff.Apply(before, got.Fragments); err != nil || !reflect.DeepEqual(res, after) {
		t.Fatalf("apply stored fragments: %v", err)
	}
}
