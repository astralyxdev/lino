package version

import "testing"

func TestOfStable(t *testing.T) {
	b := []byte("package main\n")
	v := Of(b)
	if v != Of([]byte("package main\n")) || !Valid(v) {
		t.Fatalf("unstable or invalid: %q", v)
	}
	if Of(nil) != Of([]byte{}) {
		t.Fatal("nil and empty differ")
	}
}

func TestOfChanges(t *testing.T) {
	base := "a\nb\n"
	tests := []struct {
		name  string
		other string
	}{
		{"content", "a\nc\n"},
		{"crlf", "a\r\nb\r\n"},
		{"bom", "\xef\xbb\xbfa\nb\n"},
		{"no final newline", "a\nb"},
		{"extra newline", "a\nb\n\n"},
		{"trailing space", "a \nb\n"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if Of([]byte(base)) == Of([]byte(tt.other)) {
				t.Fatalf("version unchanged for %q", tt.other)
			}
		})
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"8c21e0", true},
		{"8C21E0", false},
		{"8c21e", false},
		{"8c21e0a", false},
		{"8c21g0", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := Valid(tt.in); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
