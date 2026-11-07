// Package config defines the settings registry and the state that awgctl
// loads from the DB, checks as a whole and renders.
package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/netcheck"
)

// Setting describes one non-AWG setting.
type Setting struct {
	Key      string
	Env      string // seed env var ("" = none)
	Default  string // "" = no default (generated or detected)
	Secret   bool
	Settable bool // awgctl set/reset may change it
	Reissue  bool // a change needs a new conf for every peer
	Help     string
	// Check validates a value and returns its normal form.
	Check func(string) (string, error)
}

func checkInt(lo, hi int) func(string) (string, error) {
	return func(s string) (string, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < lo || n > hi || strconv.Itoa(n) != strings.TrimSpace(s) {
			return "", fmt.Errorf("must be an integer %d-%d", lo, hi)
		}
		return strconv.Itoa(n), nil
	}
}

func checkMTU(s string) (string, error) {
	if strings.TrimSpace(s) == "none" {
		return "none", nil
	}
	v, err := checkInt(576, 1500)(s)
	if err != nil {
		return "", fmt.Errorf("must be 576-1500 or none")
	}
	return v, nil
}

func checkBool(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "on", "yes", "1":
		return "true", nil
	case "false", "off", "no", "0":
		return "false", nil
	}
	return "", fmt.Errorf("must be true or false")
}

// Registry lists the non-AWG settings, in display order.
var Registry = []Setting{
	{Key: "server.private_key", Secret: true, Reissue: true,
		Help: "server X25519 private key; change it only with: awgctl server rotate-key",
		Check: func(s string) (string, error) {
			if !keys.Valid(s) {
				return "", fmt.Errorf("must be a base64 32-byte key")
			}
			return s, nil
		}},
	{Key: "server.endpoint_host", Env: "SERVERURL", Settable: true, Reissue: true,
		Help: "host or IP that peers dial (a DNS name or a fixed IP)",
		Check: func(s string) (string, error) {
			s = netcheck.NormalizeEndpointHost(s)
			if !netcheck.ValidEndpointHost(s) {
				return "", fmt.Errorf("%q is not a valid host name or IP address", s)
			}
			return s, nil
		}},
	{Key: "server.endpoint_port", Env: "SERVERPORT", Default: "51820", Settable: true, Reissue: true,
		Help: "port that peers dial (map it to 51820/udp in the container)",
		Check: func(s string) (string, error) {
			s = strings.TrimSpace(s)
			if !netcheck.ValidPort(s) {
				return "", fmt.Errorf("must be 1-65535 without a leading zero")
			}
			return s, nil
		}},
	{Key: "net.subnet", Env: "INTERNAL_SUBNET", Default: "10.13.13.0/24", Settable: true, Reissue: true,
		Help: "tunnel subnet, /24 only; .1 is the server", Check: netcheck.Subnet24},
	{Key: "net.mtu", Env: "MTU", Default: "1280", Settable: true,
		Help: "server tunnel MTU (576-1500, or none for the awg-quick default)", Check: checkMTU},
	{Key: "net.peer_mtu", Env: "MTU", Default: "1280", Settable: true, Reissue: true,
		Help: "MTU in the peer confs (576-1500, or none)", Check: checkMTU},
	{Key: "peer.default_allowed_ips", Env: "ALLOWEDIPS", Default: "0.0.0.0/0, ::/0", Settable: true, Reissue: true,
		Help: "what peers send into the tunnel", Check: netcheck.CIDRList},
	{Key: "peer.keepalive_seconds", Default: "25", Settable: true,
		Help: "PersistentKeepalive for peers with keepalive on", Check: checkInt(0, 65535)},
	{Key: "fw.nat_interface", Default: "eth+", Settable: true,
		Help: "interface (or + pattern) for the MASQUERADE rule",
		Check: func(s string) (string, error) {
			s = strings.TrimSpace(s)
			if !netcheck.ValidInterfacePattern(s) {
				return "", fmt.Errorf("must match ^[A-Za-z0-9_.-]{1,15}\\+?$ (for example eth0 or eth+)")
			}
			return s, nil
		}},
	{Key: "fw.ipv6_forward_drop", Default: "true", Settable: true,
		Help: "drop IPv6 forwarded through wg0 (the tunnel is IPv4-only)", Check: checkBool},
	{Key: "dns.upstream", Env: "DNS_UPSTREAM", Default: "1.1.1.1,1.0.0.1", Settable: true,
		Help: "upstream resolvers for dnsmasq: IPv4/IPv6, optional #port",
		Check: func(s string) (string, error) {
			l, err := netcheck.DNSUpstream(s)
			if err != nil {
				return "", err
			}
			return strings.Join(l, ","), nil
		}},
}

// Lookup returns the setting with key k.
func Lookup(k string) (Setting, bool) {
	for _, s := range Registry {
		if s.Key == k {
			return s, true
		}
	}
	return Setting{}, false
}
