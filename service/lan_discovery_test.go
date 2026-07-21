package service

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsVirtualInterface(t *testing.T) {
	virtual := []string{"docker0", "veth1a2b", "br-9f8e", "virbr0", "tun0", "tap3", "lo", "cni0"}
	for _, name := range virtual {
		if !isVirtualInterface(name) {
			t.Errorf("%s should be virtual", name)
		}
	}
	physical := []string{"eth0", "enp3s0", "wlan0", "eno1", "bond0"}
	for _, name := range physical {
		if isVirtualInterface(name) {
			t.Errorf("%s should not be virtual", name)
		}
	}
}

func TestSubnetHostsNarrowsLargeSubnetTo24(t *testing.T) {
	ip := net.ParseIP("10.1.2.3")
	_, ipnet, _ := net.ParseCIDR("10.1.0.0/16")
	hosts := subnetHosts(ip, ipnet)
	if len(hosts) != 254 {
		t.Fatalf("expected 254 hosts, got %d", len(hosts))
	}
	if hosts[0] != "10.1.2.1" || hosts[253] != "10.1.2.254" {
		t.Fatalf("wrong range: first=%s last=%s", hosts[0], hosts[253])
	}
}

func TestSubnetHostsSmallSubnetKept(t *testing.T) {
	ip := net.ParseIP("192.168.1.66")
	_, ipnet, _ := net.ParseCIDR("192.168.1.64/26")
	hosts := subnetHosts(ip, ipnet)
	if len(hosts) != 62 {
		t.Fatalf("expected 62 hosts, got %d", len(hosts))
	}
	if hosts[0] != "192.168.1.65" || hosts[61] != "192.168.1.126" {
		t.Fatalf("wrong range: first=%s last=%s", hosts[0], hosts[61])
	}
}

func TestSubnetHostsDegenerateCases(t *testing.T) {
	ip := net.ParseIP("192.168.1.1")
	_, slash32, _ := net.ParseCIDR("192.168.1.1/32")
	if hosts := subnetHosts(ip, slash32); len(hosts) != 0 {
		t.Fatalf("/32 should yield no hosts, got %d", len(hosts))
	}
	_, slash31, _ := net.ParseCIDR("192.168.1.0/31")
	if hosts := subnetHosts(ip, slash31); len(hosts) != 0 {
		t.Fatalf("/31 should yield no hosts, got %d", len(hosts))
	}
	v6 := net.ParseIP("fe80::1")
	_, v6net, _ := net.ParseCIDR("fe80::/64")
	if hosts := subnetHosts(v6, v6net); len(hosts) != 0 {
		t.Fatalf("IPv6 should yield no hosts, got %d", len(hosts))
	}
}

func fakePeer(t *testing.T, hostname, version string, withInfo bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ping":
			_, _ = w.Write([]byte("pong from gateway service"))
		case "/v1/gateway/device-info":
			if !withInfo {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"os":"nimoos","hostname":%q,"version":%q}`, hostname, version)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestScanHostsIdentifiesPeers(t *testing.T) {
	newPeer := fakePeer(t, "nas-new", "1.9.9", true)
	defer newPeer.Close()
	oldPeer := fakePeer(t, "", "", false) // pre-device-info NimoOS: /ping only
	defer oldPeer.Close()
	stranger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong")) // wrong fingerprint -> not NimoOS
	}))
	defer stranger.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // connection refused -> silently skipped

	urls := map[string]string{
		"10.0.0.1": newPeer.URL,
		"10.0.0.2": oldPeer.URL,
		"10.0.0.3": stranger.URL,
		"10.0.0.4": deadURL,
	}
	devices := scanHosts(
		[]string{"10.0.0.4", "10.0.0.3", "10.0.0.2", "10.0.0.1"},
		map[string]bool{"10.0.0.1": true},
		func(ip string) string { return urls[ip] },
	)

	if len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %d: %+v", len(devices), devices)
	}
	// sorted numerically by IP
	if devices[0].IP != "10.0.0.1" || devices[1].IP != "10.0.0.2" {
		t.Fatalf("wrong order: %+v", devices)
	}
	if !devices[0].Self || devices[0].Hostname != "nas-new" || devices[0].Version != "1.9.9" {
		t.Fatalf("new peer wrong: %+v", devices[0])
	}
	if devices[1].Self || devices[1].Hostname != "" || devices[1].Version != "" {
		t.Fatalf("old peer should degrade to blank info: %+v", devices[1])
	}
}
