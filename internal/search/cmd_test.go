package search

import (
	"strings"
	"testing"

	"github.com/astralyx/lino/internal/outcome"
)

func TestJoinQuery(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		words bool
		want  string
		usage bool
	}{
		{"literal one", []string{"greet world"}, false, "greet world", false},
		{"literal two", []string{"greet", "world"}, false, "", true},
		{"words one", []string{"greet world"}, true, "greet world", false},
		{"words two", []string{"greet", "world"}, true, "greet world", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := joinQuery(tt.args, tt.words)
			if tt.usage {
				e, ok := outcome.As(err)
				if !ok || e.Outcome != outcome.Usage || !strings.Contains(e.Hint, "quote") {
					t.Fatalf("err = %v, want usage with quoting hint", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
