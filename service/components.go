package service

import (
	"encoding/json"
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
	{Name: "NimoOS Core", Category: "service", VersionPath: "/v1/sys/version"},
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
	// external + ui appended in Task 16 (g.probeExternal / g.probeUI)
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
