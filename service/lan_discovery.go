package service

import (
	"encoding/binary"
	"net"
	"strings"
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
	pingFingerprint  = "pong from gateway service"
	scanProbeTimeout = 400 * time.Millisecond
	infoProbeTimeout = time.Second
	scanConcurrency  = 128
	scanAddressCap   = 1024
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
