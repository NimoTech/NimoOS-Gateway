package service

import "testing"

func TestResolveTargetLongestPrefix(t *testing.T) {
	m := &Management{
		pathTargetMap: map[string]string{
			"/v1/sys":    "http://127.0.0.1:8001",
			"/v1/search": "http://127.0.0.1:8002",
			"/v2":        "http://127.0.0.1:9999",
		},
	}
	if got := m.resolveTarget("/v1/search/version"); got != "http://127.0.0.1:8002" {
		t.Fatalf("search: got %q", got)
	}
	if got := m.resolveTarget("/v1/sys/version"); got != "http://127.0.0.1:8001" {
		t.Fatalf("sys: got %q", got)
	}
	if got := m.resolveTarget("/v1/nope/version"); got != "" {
		t.Fatalf("unmatched should be empty, got %q", got)
	}
}
