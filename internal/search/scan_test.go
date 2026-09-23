package search

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/filecmd"
	"github.com/astralyx/lino/internal/outcome"
)

func TestScanFallback(t *testing.T) {
	root := newRoot(t, fixture)
	ctx := context.Background()
	ws, err := filecmd.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenIndex(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	re := func(expr string) LineMatcher { return regexp.MustCompile(expr).MatchString }
	tests := []struct {
		name   string
		search func() (Result, error)
		hits   int
		footer string
		note   string
	}{
		{"2-char literal", func() (Result, error) { return Literal(ctx, db, "in", Options{}) },
			3, "3 hits in 3 files (scan)", ShortQueryNote},
		{"1-char literal", func() (Result, error) { return Literal(ctx, db, "#", Options{}) },
			1, "1 hit in 1 file (scan)", ShortQueryNote},
		{"dot-star regex", func() (Result, error) { return Run(ctx, db, "", re(`Err.*t`), Options{ScanNote: RegexScanNote}) },
			4, "4 hits in 3 files (scan)", RegexScanNote},
		{"short-run regex", func() (Result, error) { return Run(ctx, db, "", re(`^[a-z]{2}\b`), Options{ScanNote: RegexScanNote}) },
			0, "0 hits in 0 files (scan)", RegexScanNote},
		{"scan without reason", func() (Result, error) { return Run(ctx, db, "", re(`x`), Options{}) },
			2, "2 hits in 2 files (scan)", scanNote},
		{"indexed has no note", func() (Result, error) { return Literal(ctx, db, "Withdraw", Options{}) },
			1, "1 hit in 1 file (index)", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := tt.search()
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Hits) != tt.hits || r.Note != tt.note {
				t.Fatalf("hits %d note %q: %+v", len(r.Hits), r.Note, r)
			}
			out := Render("q", r, 200)
			var b bytes.Buffer
			if err := out.Data.(Data).WriteText(&b); err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(b.String(), tt.footer+"\n") {
				t.Fatalf("output %q, want footer %q", b.String(), tt.footer)
			}
			if tt.note != "" && !strings.Contains(out.Message, tt.note) {
				t.Fatalf("message %q lacks note", out.Message)
			}
			if tt.note == "" && strings.Contains(out.Message, "scanned") {
				t.Fatalf("unexpected note %q", out.Message)
			}
			if tt.hits == 0 && out.Outcome != outcome.Empty {
				t.Fatalf("outcome %s", out.Outcome)
			}
		})
	}
}

func TestScanNoteCLI(t *testing.T) {
	root := newRoot(t, fixture)
	out, errOut, code := run(t, root, "search", "rr")
	if code != 0 || !strings.HasSuffix(out, "(scan)\n") || !strings.Contains(errOut, ShortQueryNote) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}
