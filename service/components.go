package service

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NimoTech/NimoOS-Gateway/common"
)

// ComponentStatus is one row in GET /v1/gateway/components.
type ComponentStatus struct {
	Name     string `json:"name"`
	Category string `json:"category"` // service | external | ui
	Version  string `json:"version"`
	Status   string `json:"status"` // online | offline
	Error    string `json:"error"`
	ProbedAt string `json:"probed_at"`
}

type componentSpec struct {
	Name        string
	Category    string
	VersionPath string // service: probe path resolved against pathTargetMap
	URLFile     string // service discovered via /var/run/nimoos/<URLFile> (Parser)
	Local       bool   // Gateway itself: use common.Version, no HTTP probe
}

// componentManifest is the authoritative component list. Names are stable and
// available even when a component is offline (unlike probe results).
var componentManifest = []componentSpec{
	{Name: "Gateway", Category: "service", Local: true},
	{Name: "NimoOS Core", Category: "service", VersionPath: "/v1/sys/component/version"},
	{Name: "App Management", Category: "service", VersionPath: "/v1/apps/version"},
	{Name: "User Service", Category: "service", VersionPath: "/v1/users/version"},
	{Name: "Local Storage", Category: "service", VersionPath: "/v1/storage/version"},
	{Name: "Message Bus", Category: "service", VersionPath: "/v2/message_bus/version"},
	{Name: "AI", Category: "service", VersionPath: "/v1/ai/version"},
	{Name: "Search", Category: "service", VersionPath: "/v1/search/version"},
	{Name: "Wiki", Category: "service", VersionPath: "/v1/wiki/version"},
	{Name: "Photos", Category: "service", VersionPath: "/v1/photos/version"},
	{Name: "Terminal", Category: "service", VersionPath: "/v1/terminal/version"},
	{Name: "Parser", Category: "service", VersionPath: "/v1/parser/version", URLFile: "parser.url"},
}

// resolveTarget returns the base target URL whose registered prefix is the
// longest match for versionPath, or "" if none matches.
func (g *Management) resolveTarget(versionPath string) string {
	prefixes := make([]string, 0, len(g.pathTargetMap))
	for p := range g.pathTargetMap {
		prefixes = append(prefixes, p)
	}
	// longest prefix first, so "/v1/search" wins over "/v1"
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })
	for _, p := range prefixes {
		if strings.HasPrefix(versionPath, p) {
			return g.pathTargetMap[p]
		}
	}
	return ""
}

const probeTimeout = 2 * time.Second

// GetComponents probes every manifest component concurrently and returns the
// aggregated status list (manifest order preserved). Failures degrade to
// offline with an error message; the whole call never fails.
func (g *Management) GetComponents() []ComponentStatus {
	out := make([]ComponentStatus, len(componentManifest))
	done := make(chan struct{}, len(componentManifest))

	for i, spec := range componentManifest {
		go func(i int, spec componentSpec) {
			defer func() { done <- struct{}{} }()
			out[i] = g.probeComponent(spec)
		}(i, spec)
	}
	for range componentManifest {
		<-done
	}
	out = append(out, g.probeUI())
	out = append(out, g.probeExternal()...)
	return out
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func (g *Management) probeComponent(spec componentSpec) ComponentStatus {
	cs := ComponentStatus{Name: spec.Name, Category: spec.Category, ProbedAt: nowRFC3339()}

	if spec.Local {
		cs.Status = "online"
		cs.Version = common.Version
		return cs
	}

	var base string
	if spec.URLFile != "" {
		base = readURLFile(filepath.Join(g.State.GetRuntimePath(), spec.URLFile))
		if base == "" {
			cs.Status = "offline"
			cs.Error = "discovery file not found: " + spec.URLFile
			return cs
		}
	} else {
		base = g.resolveTarget(spec.VersionPath)
		if base == "" {
			cs.Status = "offline"
			cs.Error = "not registered"
			return cs
		}
	}

	version, err := probeVersion(strings.TrimRight(base, "/") + spec.VersionPath)
	if err != nil {
		cs.Status = "offline"
		cs.Error = err.Error()
		return cs
	}
	cs.Status = "online"
	cs.Version = version
	return cs
}

// probeVersion GETs a {name,version} endpoint and returns the version string.
func probeVersion(url string) (string, error) {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpStatusError{resp.StatusCode}
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.Version, nil
}

type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return "unexpected status " + http.StatusText(e.code)
}

func readURLFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// probeUI reads the deployed UI version from www/version.json.
func (g *Management) probeUI() ComponentStatus {
	cs := ComponentStatus{Name: "NimoOS UI", Category: "ui", ProbedAt: nowRFC3339()}
	b, err := os.ReadFile(filepath.Join(g.State.GetWWWPath(), "version.json"))
	if err != nil {
		cs.Status = "offline"
		cs.Error = "version.json not found"
		return cs
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &body); err != nil || body.Version == "" {
		cs.Status = "offline"
		cs.Error = "version.json unreadable"
		return cs
	}
	cs.Status = "online"
	cs.Version = body.Version
	return cs
}

// probeJSONVersion GETs url and extracts the value at the given JSON key.
func probeJSONVersion(url, key string) (string, error) {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpStatusError{resp.StatusCode}
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return "", err
	}
	if v, ok := m[key].(string); ok {
		return v, nil
	}
	return "", nil // reachable but no version field -> online, blank version
}

// probeReachable GETs url and returns nil if it answers HTTP 200. Used for
// services that have a health endpoint but expose no version (e.g. immich-ml's
// /ping returns the plain string "pong").
func probeReachable(url string) error {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{resp.StatusCode}
	}
	return nil
}

func (g *Management) probeExternal() []ComponentStatus {
	qdrant := ComponentStatus{Name: "Qdrant", Category: "external", ProbedAt: nowRFC3339()}
	if v, err := probeJSONVersion(strings.TrimRight(g.State.GetQdrantURL(), "/")+"/", "version"); err != nil {
		qdrant.Status, qdrant.Error = "offline", err.Error()
	} else {
		qdrant.Status, qdrant.Version = "online", v
	}

	ollama := ComponentStatus{Name: "Ollama", Category: "external", ProbedAt: nowRFC3339()}
	if v, err := probeJSONVersion(strings.TrimRight(g.State.GetOllamaURL(), "/")+"/api/version", "version"); err != nil {
		ollama.Status, ollama.Error = "offline", err.Error()
	} else {
		ollama.Status, ollama.Version = "online", v
	}

	docker := ComponentStatus{Name: "Docker", Category: "external", ProbedAt: nowRFC3339()}
	if v, err := probeDockerVersion(g.State.GetDockerSocket()); err != nil {
		docker.Status, docker.Error = "offline", err.Error()
	} else {
		docker.Status, docker.Version = "online", v
	}

	// Photos ML (immich-machine-learning): health-only via /ping. It exposes no
	// version endpoint, so Version stays blank when online.
	photosML := ComponentStatus{Name: "Photos ML", Category: "external", ProbedAt: nowRFC3339()}
	if err := probeReachable(strings.TrimRight(g.State.GetPhotosMLURL(), "/") + "/ping"); err != nil {
		photosML.Status, photosML.Error = "offline", err.Error()
	} else {
		photosML.Status = "online"
	}

	return []ComponentStatus{qdrant, ollama, docker, photosML}
}

// probeDockerVersion talks to the Docker Engine API over its unix socket.
// Go's http stdlib does not support the unix:// scheme, so we dial the socket
// via a custom Transport.
func probeDockerVersion(socket string) (string, error) {
	client := &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
	resp, err := client.Get("http://unix/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &httpStatusError{resp.StatusCode}
	}
	var body struct {
		Version string `json:"Version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.Version, nil
}
