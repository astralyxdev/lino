package e2e

import "testing"

func TestHelpGlobalFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"help_list", []string{"help"}},
		{"help_stop", []string{"help", "stop"}},
		{"help_edit", []string{"help", "edit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(t)
			r := h.Run(tt.args...)
			h.ExpectExit(r, 0)
			h.Golden(tt.name, r)
		})
	}
}
