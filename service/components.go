package service

import (
	"sort"
	"strings"
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
