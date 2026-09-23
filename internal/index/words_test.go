package index

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWordsText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"plain words here", "plain words here"},
		{"ErrInvalidAmount", "ErrInvalidAmount Err Invalid Amount"},
		{"max_withdraw-limit", "max withdraw limit"},
		{"HTTPServer.ServeHTTP()", "HTTPServer HTTP Server ServeHTTP Serve HTTP"},
		{"utf8Decode crc32", "utf8Decode utf8 Decode crc32"},
		{"x := привет_мир", "x привет мир"},
	}
	for _, tt := range tests {
		if got := WordsText(tt.in); got != tt.want {
			t.Errorf("WordsText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLineScore(t *testing.T) {
	tests := []struct {
		line, query string
		want        int
	}{
		{"return ErrInvalidAmount", "invalid amount", 4},
		{"return ErrInvalidAmount", "ErrInvalidAmount", 2},
		{"return ErrInvalidAmount", "invalidAmount", 1},
		{"invalid := amount", "invalidAmount", 1},
		{"x := invalidAmount", "invalidAmount", 2},
		{"max_withdraw = 5", "withdraw", 2},
		{"nothing here", "withdraw", 0},
		{"WITHDRAW", "withdraw", 2},
	}
	for _, tt := range tests {
		if got := lineScore(tt.line, parseWordsQuery(tt.query)); got != tt.want {
			t.Errorf("lineScore(%q, %q) = %d, want %d", tt.line, tt.query, got, tt.want)
		}
	}
}

func indexString(t *testing.T, db *DB, rel, s string) {
	t.Helper()
	if _, err := db.IndexData(context.Background(), rel, []byte(s), Stat{Size: int64(len(s)), ModTime: time.Unix(1, 0), Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchWords(t *testing.T) {
	db := mustOpen(t, newRoot(t))
	defer db.Close()
	files := map[string]string{
		"wallet/service.go": "package wallet\n\n// Withdraw moves funds out of a wallet.\nfunc Withdraw(ctx context.Context, id, amt int64) error {\n\tif amt <= 0 {\n\t\treturn ErrInvalidAmount\n\t}\n\treturn withdrawFunds(id, amt)\n}\n",
		"wallet/errors.go":  "package wallet\n\nvar ErrInvalidAmount = errors.New(\"invalid amount\")\n",
		"docs/wallet.md":    "# Wallet\n\nThe wallet service handles deposits.\nA withdraw request is checked, then the withdraw is queued.\nEvery withdraw is logged.\n",
		"api/handlers.go":   "package api\n\nfunc handle() { wallet.Withdraw(ctx, id, amt) }\n",
		"misc/readme.txt":   "unrelated text about cats\n",
		"bin/blob":          "\x00\x01withdraw",
	}
	for p, s := range files {
		indexString(t, db, p, s)
	}
	ctx := context.Background()

	paths := func(r WordsResult) []string {
		var out []string
		for _, f := range r.Files {
			out = append(out, f.Path)
		}
		return out
	}

	t.Run("ranking", func(t *testing.T) {
		r, err := db.SearchWords(ctx, "withdraw", WordsOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := paths(r)
		if len(got) != 3 || got[0] != "docs/wallet.md" {
			t.Fatalf("got %v, want docs/wallet.md first of 3", got)
		}
		for i := 1; i < len(r.Files); i++ {
			if r.Files[i].Score > r.Files[i-1].Score {
				t.Errorf("scores not descending: %v", r.Files)
			}
		}
		if r.Files[0].Hits[0].Line != 4 || len(r.Files[0].Hits) != 2 {
			t.Errorf("docs hits = %+v", r.Files[0].Hits)
		}
	})

	t.Run("multi-word ranks files with all words first", func(t *testing.T) {
		r, err := db.SearchWords(ctx, "invalid amount", WordsOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := paths(r)
		if len(got) != 2 || got[0] != "wallet/errors.go" {
			t.Fatalf("got %v", got)
		}
		want := []WordHit{{3, `var ErrInvalidAmount = errors.New("invalid amount")`}}
		if !reflect.DeepEqual(r.Files[0].Hits, want) {
			t.Errorf("hits = %+v", r.Files[0].Hits)
		}
	})

	t.Run("camelCase query matches parts", func(t *testing.T) {
		r, err := db.SearchWords(ctx, "withdrawFunds", WordsOptions{PerFile: 1})
		if err != nil {
			t.Fatal(err)
		}
		if got := paths(r); !reflect.DeepEqual(got, []string{"wallet/service.go"}) {
			t.Fatalf("got %v", got)
		}
		if h := r.Files[0].Hits; len(h) != 1 || h[0].Line != 8 {
			t.Errorf("hits = %+v", h)
		}
	})

	t.Run("no hits is empty", func(t *testing.T) {
		for _, q := range []string{"zebra", "", "  ;; "} {
			r, err := db.SearchWords(ctx, q, WordsOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Files) != 0 || r.Hits != 0 || r.More {
				t.Errorf("%q: got %+v", q, r)
			}
		}
	})

	t.Run("limits", func(t *testing.T) {
		r, err := db.SearchWords(ctx, "withdraw", WordsOptions{Limit: 2, PerFile: 1})
		if err != nil {
			t.Fatal(err)
		}
		if r.Hits != 2 || len(r.Files) != 2 || !r.More {
			t.Errorf("got %+v", r)
		}
	})

	t.Run("path filter", func(t *testing.T) {
		r, err := db.SearchWords(ctx, "withdraw", WordsOptions{Match: func(p string) bool {
			return strings.HasPrefix(p, "wallet/") || strings.HasPrefix(p, "api/")
		}})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(paths(r), ",")
		if strings.Contains(got, "docs/") || len(r.Files) != 2 {
			t.Errorf("got %v", got)
		}
	})

	t.Run("reindex and remove keep words in sync", func(t *testing.T) {
		indexString(t, db, "misc/readme.txt", "now it mentions withdraw too\n")
		if _, err := db.RemoveFile(ctx, "docs/wallet.md"); err != nil {
			t.Fatal(err)
		}
		r, err := db.SearchWords(ctx, "withdraw", WordsOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(paths(r), ",")
		if strings.Contains(got, "docs/") || !strings.Contains(got, "misc/readme.txt") {
			t.Errorf("got %v", got)
		}
		r, _ = db.SearchWords(ctx, "cats", WordsOptions{})
		if len(r.Files) != 0 {
			t.Errorf("stale words for replaced content: %+v", r)
		}
	})
}
