package netcheck

import (
	"strings"
	"testing"
)

func TestValidators(t *testing.T) {
	for _, c := range []struct {
		f    func(string) bool
		in   string
		want bool
	}{
		{IsIPv4, "10.0.0.1", true}, {IsIPv4, "010.0.0.1", false}, {IsIPv4, "1.2.3", false},
		{IsIPv6, "2001:db8::1", true}, {IsIPv6, "fe80::1%eth0", false}, {IsIPv6, "1.2.3.4", false},
		{ValidPort, "51820", true}, {ValidPort, "08", false}, {ValidPort, "65536", false}, {ValidPort, "0", false},
		{ValidEndpointHost, "vpn.example.com", true}, {ValidEndpointHost, "[2001:db8::1]", true},
		{ValidEndpointHost, "2001:db8::1", false}, {ValidEndpointHost, "999.1.1.1", false},
		{ValidEndpointHost, "bad;host", false}, {ValidEndpointHost, "-a.example", false},
		{ValidInterfacePattern, "eth+", true}, {ValidInterfacePattern, "eth0", true},
		{ValidInterfacePattern, "eth0;reboot", false}, {ValidInterfacePattern, "$(id)", false},
		{ValidInterfacePattern, "a b", false}, {ValidInterfacePattern, "verylonginterface0", false},
	} {
		if got := c.f(c.in); got != c.want {
			t.Errorf("%q: got %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSubnet24(t *testing.T) {
	for in, want := range map[string]string{"10.13.13.0": "10.13.13.0/24", "10.8.0.0/24": "10.8.0.0/24"} {
		if got, err := Subnet24(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"10.13.0.0/16", "10.13.13.5", "10.13.13.0/25", "nope"} {
		if _, err := Subnet24(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if Prefix24("10.13.13.0/24") != "10.13.13" {
		t.Error("Prefix24")
	}
}

func TestLists(t *testing.T) {
	if got, err := CIDRList("0.0.0.0/0,::/0"); err != nil || got != "0.0.0.0/0, ::/0" {
		t.Errorf("CIDRList: %q %v", got, err)
	}
	if _, err := CIDRList("10.0.0.1/24"); err == nil {
		t.Error("host bits accepted")
	}
	if got, err := DNSUpstream(" 1.1.1.1 , 2606:4700::1111#53"); err != nil || len(got) != 2 {
		t.Errorf("DNSUpstream: %v %v", got, err)
	}
	for _, bad := range []string{"dns.example", "1.1.1.1#0", ""} {
		if _, err := DNSUpstream(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEndpointHostForms(t *testing.T) {
	if !ValidEndpointHost("203.0.113.7") {
		t.Error("an IPv4 address was refused")
	}
	for in, want := range map[string]string{
		" 2001:db8::1 ":   "[2001:db8::1]",
		"[2001:db8::1]":   "[2001:db8::1]",
		"vpn.example.com": "vpn.example.com",
		"203.0.113.7\n":   "203.0.113.7",
	} {
		if got := NormalizeEndpointHost(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestCIDRListErrors(t *testing.T) {
	if got, err := CIDRList(" 10.0.0.0/8, ,192.168.0.0/16,"); err != nil || got != "10.0.0.0/8, 192.168.0.0/16" {
		t.Errorf("empty parts: %q %v", got, err)
	}
	for in, want := range map[string]string{
		"10.0.0.0/8, nope": `"nope" is not an IP prefix`,
		"10.0.0.1/24":      "has host bits set; use 10.0.0.0/24",
		" , ":              "the list is empty",
	} {
		if _, err := CIDRList(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", in, err, want)
		}
	}
}
