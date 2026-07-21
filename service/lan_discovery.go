package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// LanDevice is one discovered NimoOS host in GET /v1/gateway/lan-discovery.
type LanDevice struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
	Self     bool   `json:"self"`
}

// LanDiscoveryResult is the response body of GET /v1/gateway/lan-discovery.
type LanDiscoveryResult struct {
	Devices   []LanDevice `json:"devices"`
	Truncated bool        `json:"truncated"`
}

const (
	// pingFingerprint is the exact body of GET /ping on every NimoOS gateway,
	// including versions deployed long before this feature existed.
	pingFingerprint   = "pong from gateway service"
	scanProbeTimeout  = 400 * time.Millisecond
	infoProbeTimeout  = time.Second
	scanConcurrency   = 128
	scanAddressCap    = 1024
	scanOverallBudget = 5 * time.Second
)

// virtualIfacePrefixes name interface families that never face the LAN.
var virtualIfacePrefixes = []string{"docker", "veth", "br-", "virbr", "tun", "tap", "lo", "cni", "flannel"}

func isVirtualInterface(name string) bool {
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// subnetHosts lists the IPv4 host addresses to scan for one interface address,
// narrowed to the /24 containing ip when the subnet is larger than /24.
// Network and broadcast addresses are excluded; IPv6 and /31+/32 yield nothing.
func subnetHosts(ip net.IP, ipnet *net.IPNet) []string {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil
	}
	if ones < 24 {
		ones = 24
	}
	if ones >= 31 {
		return nil
	}
	base := binary.BigEndian.Uint32(ip4.Mask(net.CIDRMask(ones, 32)))
	total := uint32(1) << (32 - ones)
	hosts := make([]string, 0, total-2)
	for i := uint32(1); i < total-1; i++ {
		addr := make(net.IP, 4)
		binary.BigEndian.PutUint32(addr, base+i)
		hosts = append(hosts, addr.String())
	}
	return hosts
}

// enumerateScanTargets derives the scan scope from local interfaces only —
// never from caller input (SSRF guard). Returns the deduplicated host list,
// the set of this machine's own IPv4 addresses, and whether the list was
// capped at scanAddressCap.
func enumerateScanTargets() (hosts []string, selfIPs map[string]bool, truncated bool) {
	selfIPs = map[string]bool{}
	seen := map[string]bool{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, selfIPs, false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue
			}
			selfIPs[ip4.String()] = true
			for _, h := range subnetHosts(ip4, ipnet) {
				if seen[h] {
					continue
				}
				if len(hosts) >= scanAddressCap {
					return hosts, selfIPs, true
				}
				seen[h] = true
				hosts = append(hosts, h)
			}
		}
	}
	return hosts, selfIPs, false
}

// probePeer checks whether baseURL hosts a NimoOS gateway by fingerprinting
// GET /ping, then enriches the hit via its unauthenticated device-info
// endpoint. Old NimoOS versions have no device-info: they still match the
// fingerprint and degrade to blank hostname/version.
func probePeer(ctx context.Context, baseURL, ip string) (LanDevice, bool) {
	client := &http.Client{Timeout: scanProbeTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/ping", nil)
	if err != nil {
		return LanDevice{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return LanDevice{}, false
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256))
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != http.StatusOK || string(body) != pingFingerprint {
		return LanDevice{}, false
	}

	dev := LanDevice{IP: ip}
	infoClient := &http.Client{Timeout: infoProbeTimeout}
	infoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/gateway/device-info", nil)
	if err != nil {
		return dev, true
	}
	infoResp, err := infoClient.Do(infoReq)
	if err != nil {
		return dev, true
	}
	defer infoResp.Body.Close()
	if infoResp.StatusCode != http.StatusOK {
		return dev, true
	}
	var info struct {
		OS       string `json:"os"`
		Hostname string `json:"hostname"`
		Version  string `json:"version"`
	}
	if json.NewDecoder(io.LimitReader(infoResp.Body, 4096)).Decode(&info) == nil && info.OS == "nimoos" {
		dev.Hostname, dev.Version = info.Hostname, info.Version
	}
	return dev, true
}

// scanHosts probes every host concurrently (bounded by scanConcurrency) and
// returns the NimoOS hits sorted numerically by IP. baseURLFor maps a host IP
// to the URL to probe — production uses "http://"+ip, tests point at httptest
// servers.
func scanHosts(ctx context.Context, hosts []string, selfIPs map[string]bool, baseURLFor func(string) string) []LanDevice {
	sem := make(chan struct{}, scanConcurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup
	devices := []LanDevice{}
	for _, h := range hosts {
		wg.Add(1)
		go func(h string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			dev, ok := probePeer(ctx, baseURLFor(h), h)
			if !ok {
				return
			}
			dev.Self = selfIPs[h]
			mu.Lock()
			devices = append(devices, dev)
			mu.Unlock()
		}(h)
	}
	wg.Wait()
	sort.Slice(devices, func(i, j int) bool { return ipLess(devices[i].IP, devices[j].IP) })
	return devices
}

func ipLess(a, b string) bool {
	pa, pb := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if pa == nil || pb == nil {
		return a < b
	}
	return bytes.Compare(pa, pb) < 0
}

// ScanLAN discovers NimoOS gateways on the local /24 subnets. Scan scope is
// derived from local interfaces only — never from caller input.
func (g *Management) ScanLAN() LanDiscoveryResult {
	ctx, cancel := context.WithTimeout(context.Background(), scanOverallBudget)
	defer cancel()
	hosts, selfIPs, truncated := enumerateScanTargets()
	devices := scanHosts(ctx, hosts, selfIPs, func(ip string) string { return "http://" + ip })
	return LanDiscoveryResult{Devices: devices, Truncated: truncated}
}
