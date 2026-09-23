package historycmd

import (
	"reflect"
	"testing"
	"time"

	"github.com/astralyx/lino/internal/changelog"
	"github.com/astralyx/lino/internal/history"
)

func TestRanges(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []shape
		want []changelog.Range
	}{
		{"none", nil, nil},
		{"replace", []shape{{12, 3, 3}}, []changelog.Range{{Start: 13, End: 15}}},
		{"delete", []shape{{0, 1, 0}}, []changelog.Range{{Start: 1, End: 0}}},
		{"shifted", []shape{{1, 0, 2}, {9, 1, 1}}, []changelog.Range{{Start: 2, End: 3}, {Start: 12, End: 12}}},
	} {
		if got := ranges(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEntrySummary(t *testing.T) {
	for _, c := range []struct {
		name string
		ch   history.Change
		fs   []shape
		want string
	}{
		{"edit", history.Change{Op: "edit"}, []shape{{12, 3, 3}}, "lines 13-15"},
		{"created", history.Change{Op: "write", Extra: history.Extra{Created: true}}, []shape{{0, 0, 9}}, "created"},
		{"removed", history.Change{Op: "rm", Extra: history.Extra{Removed: true}}, []shape{{0, 9, 0}}, "removed"},
		{"moved", history.Change{Op: "mv", Extra: history.Extra{From: "a.go"}}, nil, "moved from a.go"},
		{"binary", history.Change{Op: "external", Extra: history.Extra{Binary: true}}, nil, "binary"},
		{"rollback", history.Change{Op: "rollback", Extra: history.Extra{Undid: 7}}, []shape{{0, 1, 1}}, "undid 7, lines 1"},
		{"rollback rm", history.Change{Op: "rollback", Extra: history.Extra{Undid: 7, Created: true}}, nil, "undid 7, created"},
	} {
		if got := entry(c.ch, c.fs).Summary; got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStamp(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-time.Hour), "11:00:00"},
		{now.Add(-24 * time.Hour), "2026-09-22 12:00:00"},
	} {
		if got := stamp(c.t, now); got != c.want {
			t.Errorf("stamp(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}
