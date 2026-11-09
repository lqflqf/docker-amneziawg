// Package netcheck validates the network values that end up in confs: IP
// addresses, ports, host names, CIDR lists and interface patterns.
package netcheck

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// IsIPv4 reports whether s is a dotted-quad IPv4 address without leading zeros.
func IsIPv4(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4() && a.String() == s
}

// IsIPv6 reports whether s is an IPv6 address without brackets or zone.
func IsIPv6(s string) bool {
	if !strings.Contains(s, ":") || strings.Contains(s, "%") {
		return false
	}
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is6()
}

// ValidPort reports whether s is 1-65535 in decimal without a leading zero.
func ValidPort(s string) bool {
	if s == "" || s[0] == '0' || len(s) > 5 {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

const label = `[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?`

var (
	hostRe   = regexp.MustCompile(`^(` + label + `\.)*` + label + `\.?$`)
	digitsRe = regexp.MustCompile(`^[0-9.]+$`)
)

// ValidEndpointHost reports whether s is a DNS name, an IPv4 address or a
// bracketed IPv6 address.
func ValidEndpointHost(s string) bool {
	if IsIPv4(s) {
		return true
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return IsIPv6(s[1 : len(s)-1])
	}
	if strings.Contains(s, ":") {
		return false
	}
	// Digits and dots that are not a valid IPv4 address (999.1.1.1) are not a host.
	if digitsRe.MatchString(s) {
		return false
	}
	return len(s) <= 253 && hostRe.MatchString(s)
}

// NormalizeEndpointHost puts brackets around a bare IPv6 address.
func NormalizeEndpointHost(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ":") && !strings.HasPrefix(s, "[") {
		return "[" + s + "]"
	}
	return s
}

// Subnet24 parses "10.13.13.0" or "10.13.13.0/24" and returns "10.13.13.0/24".
// Any other prefix length, or a host part other than .0, is an error.
func Subnet24(s string) (string, error) {
	s = strings.TrimSpace(s)
	addr := s
	if i := strings.IndexByte(s, '/'); i >= 0 {
		if s[i+1:] != "24" {
			return "", fmt.Errorf("only a /24 subnet is supported, got %q", s)
		}
		addr = s[:i]
	}
	if !IsIPv4(addr) {
		return "", fmt.Errorf("%q is not an IPv4 network such as 10.13.13.0/24", s)
	}
	if !strings.HasSuffix(addr, ".0") {
		return "", fmt.Errorf("%q: the host part must be .0 (for example 10.13.13.0/24)", s)
	}
	return addr + "/24", nil
}

// Prefix24 returns the first three octets of a /24 subnet: "10.13.13".
func Prefix24(subnet string) string {
	addr := strings.TrimSuffix(subnet, "/24")
	return addr[:strings.LastIndexByte(addr, '.')]
}

// CIDRList validates a comma-separated list of IPv4/IPv6 prefixes and returns
// it in the "a, b" form.
func CIDRList(s string) (string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return "", fmt.Errorf("%q is not an IP prefix such as 0.0.0.0/0 or 192.168.1.0/24", part)
		}
		if p.Masked() != p {
			return "", fmt.Errorf("%q has host bits set; use %s", part, p.Masked())
		}
		out = append(out, p.String())
	}
	if len(out) == 0 {
		return "", fmt.Errorf("the list is empty")
	}
	return strings.Join(out, ", "), nil
}

// DNSUpstream validates a comma-separated list of IPv4/IPv6 addresses, each
// with an optional #port.
func DNSUpstream(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.Join(strings.Fields(part), "")
		if part == "" {
			continue
		}
		addr, port, hasPort := strings.Cut(part, "#")
		if !IsIPv4(addr) && !IsIPv6(addr) {
			return nil, fmt.Errorf("DNS upstream %q is not an IPv4 or IPv6 address (host names are not supported)", part)
		}
		if hasPort && !ValidPort(port) {
			return nil, fmt.Errorf("DNS upstream %q has an invalid port (use address#port, 1-65535)", part)
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the DNS upstream list is empty")
	}
	return out, nil
}

var ifacePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}\+?$`)

// ValidInterfacePattern reports whether s is an iptables interface name or a
// "+" pattern (eth+). The value goes into a PostUp line that awg-quick runs
// through bash, so the pattern is strict.
func ValidInterfacePattern(s string) bool {
	return ifacePattern.MatchString(s)
}
