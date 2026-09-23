package history

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	fver "github.com/astralyx/lino/internal/version"
)

// snapshot records every version a sim produced, per path, so a test can
// check which still reconstruct.
type snapshot struct {
	path, content string
	id            int64 // change that produced it
}

func (s *sim) snap(path string) snapshot {
	id, err := s.st.Latest(context.Background())
	if err != nil {
		s.tb.Fatal(err)
	}
	return snapshot{path, string(s.files[path]), id}
}

// checkReconstruct asserts that every version produced after change `after`
// reconstructs (retained) and returns how many of the older ones do.
func checkReconstruct(t *testing.T, s *sim, snaps []snapshot, after int64) int {
	t.Helper()
	ctx := context.Background()
	old := 0
	for _, sn := range snaps {
		cur, ok := s.files[sn.path]
		if ok && string(cur) == sn.content {
			continue // current version: trivially found
		}
		v, err := s.st.Reconstruct(ctx, s.current, sn.path, fver.Of([]byte(sn.content)))
		if sn.id <= after {
			if err == nil {
				old++
			}
			continue
		}
		if err != nil {
			t.Errorf("version of %s from change %d: %v", sn.path, sn.id, err)
			continue
		}
		if string(v.Content) != sn.content {
			t.Errorf("version of %s from change %d = %q, want %q", sn.path, sn.id, v.Content, sn.content)
		}
	}
	return old
}

func minID(t *testing.T, st *Store) int64 {
	t.Helper()
	var id int64
	if err := st.SQL.QueryRow(`SELECT coalesce(min(id), 0) FROM changes`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// history builds a chain of mixed operations on a few files and returns the
// versions it produced.
func buildHistory(s *sim, n int, fill func(i int) string) []snapshot {
	var snaps []snapshot
	for i := range n {
		p := fmt.Sprintf("f%d.txt", i%3)
		switch {
		case i%11 == 10:
			if _, ok := s.files[p]; ok {
				s.rm(p)
				continue
			}
		case i%7 == 6:
			if _, ok := s.files[p]; ok {
				q := p + ".m"
				if _, taken := s.files[q]; !taken {
					s.mv(p, q)
					snaps = append(snaps, s.snap(q))
					s.set(p, fill(i)+"\n", OpWrite)
					snaps = append(snaps, s.snap(p))
					continue
				}
			}
		}
		lines := strings.Split(strings.TrimSuffix(string(s.files[p]), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}
		lines = append(lines, fill(i))
		if len(lines) > 6 {
			lines = lines[len(lines)-6:]
		}
		s.set(p, strings.Join(lines, "\n")+"\n", OpEdit)
		snaps = append(snaps, s.snap(p))
	}
	return snaps
}

func TestPruneAge(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		maxAge time.Duration
		want   string // none, old, all
	}{
		{"nothing old enough", 30 * 24 * time.Hour, "none"},
		{"older half", 14 * 24 * time.Hour, "old"},
		{"all", time.Hour, "all"},
		{"no limit", 0, "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSim(t)
			snaps := buildHistory(s, 40, func(i int) string { return fmt.Sprintf("line %d", i) })
			total, _ := s.st.Latest(ctx)
			half := total / 2
			// The older half is 20 days old, the rest 1 day; one out-of-order
			// timestamp in the old half must not punch a hole in the middle.
			if _, err := s.st.SQL.Exec(`UPDATE changes SET time = CASE WHEN id <= ? THEN ? ELSE ? END`,
				half, now.Add(-20*24*time.Hour).UnixNano(), now.Add(-24*time.Hour).UnixNano()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.st.SQL.Exec(`UPDATE changes SET time = ? WHERE id = 5`, now.UnixNano()); err != nil {
				t.Fatal(err)
			}
			res, err := s.st.Prune(ctx, Retention{MaxAge: tt.maxAge, Now: now})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]int64{"none": 0, "old": half, "all": total}[tt.want]
			var left int64
			s.st.SQL.QueryRow(`SELECT count(*) FROM changes`).Scan(&left)
			if int64(res.Removed) != want || left != total-want || res.UpTo != want {
				t.Fatalf("removed %d up to %d (left %d), want %d of %d", res.Removed, res.UpTo, left, want, total)
			}
			if left > 0 && minID(t, s.st) != want+1 {
				t.Fatalf("min id %d, want %d: not pruned from the oldest end", minID(t, s.st), want+1)
			}
			var frags int
			s.st.SQL.QueryRow(`SELECT count(*) FROM fragments WHERE change_id <= ?`, res.UpTo).Scan(&frags)
			if frags != 0 {
				t.Fatalf("%d orphan fragments", frags)
			}
			checkReconstruct(t, s, snaps, res.UpTo)
		})
	}
}

func TestPruneSize(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(1, 2))
	noise := func(int) string {
		var b strings.Builder
		for range 400 {
			b.WriteByte(byte('!' + rng.IntN(90)))
		}
		return b.String()
	}
	tests := []struct {
		name    string
		maxSize int64
	}{
		{"generous", 64 << 20},
		{"half", 0}, // set from the measured size
		{"tiny", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSim(t)
			snaps := buildHistory(s, 300, noise)
			used, _, err := s.st.pages(ctx)
			if err != nil {
				t.Fatal(err)
			}
			max := tt.maxSize
			if max == 0 {
				max = used / 2
			}
			res, err := s.st.Prune(ctx, Retention{MaxSize: max})
			if err != nil {
				t.Fatal(err)
			}
			latest, _ := s.st.Latest(ctx)
			switch tt.name {
			case "generous":
				if res.Removed != 0 {
					t.Fatalf("removed %d under a generous limit", res.Removed)
				}
			case "half":
				if res.Used > max {
					t.Fatalf("used %d after prune, limit %d", res.Used, max)
				}
				if res.Removed == 0 || latest == 0 {
					t.Fatalf("removed %d, latest %d: want a partial prune", res.Removed, latest)
				}
				// Not wildly more than needed: at least a third survives.
				var left int
				s.st.SQL.QueryRow(`SELECT count(*) FROM changes`).Scan(&left)
				if left < res.Removed/3 {
					t.Fatalf("left %d, removed %d: pruned far past the limit", left, res.Removed)
				}
				if minID(t, s.st) != res.UpTo+1 {
					t.Fatalf("min id %d after pruning up to %d", minID(t, s.st), res.UpTo)
				}
			case "tiny":
				if latest != 0 {
					t.Fatalf("latest %d, want every change pruned", latest)
				}
			}
			checkReconstruct(t, s, snaps, res.UpTo)
		})
	}
}

func TestPruneKeepsNewIDs(t *testing.T) {
	ctx := context.Background()
	s := newSim(t)
	buildHistory(s, 10, func(i int) string { return fmt.Sprint(i) })
	before, _ := s.st.Latest(ctx)
	if _, err := s.st.Prune(ctx, Retention{MaxAge: time.Nanosecond, Now: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	id, err := s.st.Add(ctx, Change{ID: before + 1, Source: SourceLino, Path: "x", Op: OpWrite})
	if err != nil || id != before+1 {
		t.Fatalf("add after prune: id %d err %v", id, err)
	}
}

func TestMaybePrune(t *testing.T) {
	ctx := context.Background()
	s := newSim(t)
	buildHistory(s, 10, func(i int) string { return fmt.Sprint(i) })
	total, _ := s.st.Latest(ctx)
	now := time.Now().Add(48 * time.Hour)
	r := Retention{MaxAge: 24 * time.Hour, Now: now}
	steps := []struct {
		at  time.Time
		ran bool
	}{
		{now, true},
		{now.Add(30 * time.Minute), false},
		{now.Add(2 * time.Hour), true},
	}
	for i, st := range steps {
		r.Now = st.at
		res, ran, err := s.st.MaybePrune(ctx, r, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if ran != st.ran {
			t.Fatalf("step %d: ran %v, want %v", i, ran, st.ran)
		}
		if i == 0 && int64(res.Removed) != total {
			t.Fatalf("first prune removed %d, want %d", res.Removed, total)
		}
	}
}

func TestVacuum(t *testing.T) {
	ctx := context.Background()
	s := newSim(t)
	rng := rand.New(rand.NewPCG(3, 4))
	buildHistory(s, 200, func(int) string {
		b := make([]byte, 2000)
		for i := range b {
			b[i] = byte('!' + rng.IntN(90))
		}
		return string(b)
	})
	defer func(v int64) { VacuumMinFree = v }(VacuumMinFree)
	VacuumMinFree = 1 << 40
	if _, err := s.st.Prune(ctx, Retention{MaxSize: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.st.Vacuum(ctx); err != nil || ok {
		t.Fatalf("vacuum below the minimum: %v %v", ok, err)
	}
	VacuumMinFree = 1
	if ok, err := s.st.Vacuum(ctx); err != nil || !ok {
		t.Fatalf("vacuum: %v %v", ok, err)
	}
	_, free, _ := s.st.pages(ctx)
	if free != 0 {
		t.Fatalf("free %d after vacuum", free)
	}
}
