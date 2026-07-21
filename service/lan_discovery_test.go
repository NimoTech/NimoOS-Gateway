package service

import (
	"net"
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
