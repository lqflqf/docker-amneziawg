// Package health is the Docker health check. It reads only /run and the
// running system, never the DB.
package health

import (
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
)

// Check returns nil when wg0 is up and dnsmasq answers on the tunnel address.
func Check(ctx context.Context, p sys.Paths, r sys.Runner) error {
	out, _, err := r.Run(ctx, "awg", []string{"show", "interfaces"}, nil, nil)
	if err != nil || !slices.Contains(strings.Fields(string(out)), render.Interface) {
		return fmt.Errorf("tunnel %s is down", render.Interface)
	}
	state, _ := os.ReadFile(p.DNSState())
	switch s := strings.TrimSpace(string(state)); s {
	case "running":
	case "invalid":
		return fmt.Errorf("dnsmasq has no valid config, see the container log")
	default:
		if s == "" {
			s = "none"
		}
		return fmt.Errorf("dnsmasq is not running (state: %s)", s)
	}
	b, _ := os.ReadFile(p.DNSAddress())
	addr := strings.TrimSpace(string(b))
	ip := net.ParseIP(addr)
	if ip == nil {
		return fmt.Errorf("no tunnel address in %s", p.DNSAddress())
	}
	if !onInterface(ip, render.Interface) {
		return fmt.Errorf("the tunnel address %s is not on %s", addr, render.Interface)
	}
	return Query(ctx, addr)
}

// Query asks addr:53 for the health record over UDP.
func Query(ctx context.Context, addr string) error {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		return d.DialContext(ctx, "udp", net.JoinHostPort(addr, "53"))
	}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := r.LookupHost(ctx, "health.amneziawg.")
	if err != nil || len(ips) == 0 {
		return fmt.Errorf("dnsmasq does not answer on %s", addr)
	}
	return nil
}

func onInterface(ip net.IP, name string) bool {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return false
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}
