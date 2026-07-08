package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
	if got := m.resolveTarget("/v1/sys/component/version"); got != "http://127.0.0.1:8001" {
		t.Fatalf("sys: got %q", got)
	}
	if got := m.resolveTarget("/v1/nope/version"); got != "" {
		t.Fatalf("unmatched should be empty, got %q", got)
	}
}

func TestGetComponentsServiceOnlineOffline(t *testing.T) {
	// mock a healthy service that answers its version path
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/search/version" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"Search","version":"9.9.9"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	m := &Management{
		pathTargetMap: map[string]string{
			"/v1/search": srv.URL,
			// /v1/sys points nowhere -> offline
			"/v1/sys": "http://127.0.0.1:1", // connection refused fast
		},
		State: &State{}, // runtimePath empty -> parser offline, fine here
	}

	comps := m.GetComponents()

	byName := map[string]ComponentStatus{}
	for _, c := range comps {
		byName[c.Name] = c
	}

	if c := byName["Search"]; c.Status != "online" || c.Version != "9.9.9" {
		t.Fatalf("Search: %+v", c)
	}
	if c := byName["NimoOS Core"]; c.Status != "offline" || c.Error == "" {
		t.Fatalf("Core should be offline with error: %+v", c)
	}
	if c := byName["Gateway"]; c.Status != "online" {
		t.Fatalf("Gateway (Local) should be online: %+v", c)
	}
}

func TestProbeUIReadsVersionJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "version.json"), []byte(`{"version":"ui-1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &State{}
	st.SetWWWPath(dir)
	m := &Management{State: st}

	cs := m.probeUI()
	if cs.Category != "ui" || cs.Status != "online" || cs.Version != "ui-1.2.3" {
		t.Fatalf("ui probe: %+v", cs)
	}
}
