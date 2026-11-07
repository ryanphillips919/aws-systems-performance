package main

import "testing"

func TestBackendName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		bad      bool
	}{{"", "postgres", false}, {"postgres", "postgres", false}, {"dynamodb", "dynamodb", false}, {"anything", "", true}} {
		got, err := backendName(tc.in)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("%q: %q %v", tc.in, got, err)
		}
	}
}
