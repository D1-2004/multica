package handler

import "testing"

func TestRuntimeSlug(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"FC-Hermes", "fc-hermes"},
		{"  FC  Hermes!!!  ", "fc-hermes"},
		{"中文 Hermes", "hermes"},
		{"中文", "fc-hermes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeSlug(tc.name); got != tc.want {
				t.Fatalf("runtimeSlug(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
