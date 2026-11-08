package main

import "testing"

func TestBackendName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		bad      bool
	}{{"", "postgres", false}, {"postgres", "postgres", false}, {"dynamodb", "dynamodb", false}, {"sharded-postgres", "sharded-postgres", false}, {"anything", "", true}} {
		got, err := backendName(tc.in)
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("%q: %q %v", tc.in, got, err)
		}
	}
}

func TestCacheEnabledConfiguration(t *testing.T) {
	t.Setenv("CACHE_ENABLED", "")
	if v, err := boolEnv("CACHE_ENABLED", false); err != nil || v {
		t.Fatalf("default: %v %v", v, err)
	}
	t.Setenv("CACHE_ENABLED", "true")
	if v, err := boolEnv("CACHE_ENABLED", false); err != nil || !v {
		t.Fatalf("enabled: %v %v", v, err)
	}
	t.Setenv("CACHE_ENABLED", "bad")
	if _, err := boolEnv("CACHE_ENABLED", false); err == nil {
		t.Fatal("invalid value accepted")
	}
}
