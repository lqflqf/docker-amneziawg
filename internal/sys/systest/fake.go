// Package systest has a fake sys.Runner that models awg, awg-quick, dnsmasq
// and s6-svc well enough for unit tests, and records every call.
package systest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// Fake is a fake runner. Set Up to model a running wg0; set Fail to make a
// command fail (key: call prefix such as "awg syncconf", value: stderr).
type Fake struct {
	mu    sync.Mutex
	Up    bool
	Peers []string // public keys that wg0 has
	Fail  map[string]string
	Calls []string
	// Ghost stays in wg0 after each syncconf, to model a failed removal.
	Ghost string
}

// New returns a fake with wg0 down.
func New() *Fake { return &Fake{Fail: map[string]string{}} }

var quickKeys = map[string]bool{"address": true, "dns": true, "mtu": true, "table": true,
	"preup": true, "postup": true, "predown": true, "postdown": true, "saveconfig": true}

func strip(text string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		k, _, _ := strings.Cut(l, "=")
		if quickKeys[strings.ToLower(strings.TrimSpace(k))] {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func peersOf(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(l, "=")
		if ok && strings.TrimSpace(k) == "PublicKey" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

// Run implements sys.Runner.
func (f *Fake) Run(_ context.Context, name string, args []string, _ []byte, _ []string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.Calls = append(f.Calls, call)
	for prefix, msg := range f.Fail {
		if strings.HasPrefix(call, prefix) {
			return nil, []byte(msg), fmt.Errorf("exit status 1")
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	switch {
	case name == "awg-quick" && len(args) == 2 && args[0] == "strip":
		return []byte(strip(read(args[1]))), nil, nil
	case name == "awg" && len(args) == 3 && args[0] == "setconf":
		text := read(args[2])
		if strings.Contains(text, "Bogus") || strings.Contains(text, "Address") {
			return nil, []byte("Line unrecognized: `Bogus'"), fmt.Errorf("exit status 1")
		}
		return nil, []byte("Unable to modify interface: No such device"), fmt.Errorf("exit status 1")
	case name == "dnsmasq" && len(args) >= 1 && args[0] == "--test":
		if strings.Contains(read(args[len(args)-1]), "bogus-option") {
			return nil, []byte("dnsmasq: bad option"), fmt.Errorf("exit status 1")
		}
		return nil, []byte("dnsmasq: syntax check OK."), nil
	case name == "awg" && len(args) == 2 && args[0] == "show" && args[1] == "wg0":
		if !f.Up {
			return nil, []byte("Unable to access interface: No such device"), fmt.Errorf("exit status 1")
		}
		return []byte("interface: wg0\n"), nil, nil
	case name == "awg" && len(args) == 2 && args[0] == "show" && args[1] == "interfaces":
		if f.Up {
			return []byte("wg0\n"), nil, nil
		}
		return []byte("\n"), nil, nil
	case name == "awg" && len(args) == 3 && args[0] == "show" && args[2] == "peers":
		return []byte(strings.Join(f.Peers, "\n") + "\n"), nil, nil
	case name == "awg" && len(args) == 3 && args[0] == "show" && args[2] == "dump":
		if !f.Up {
			return nil, nil, fmt.Errorf("exit status 1")
		}
		out := "priv\tpub\t51820\toff\n"
		for _, p := range f.Peers {
			out += p + "\tpsk\t(none)\t10.0.0.2/32\t0\t0\t0\toff\n"
		}
		return []byte(out), nil, nil
	case name == "awg" && len(args) == 3 && args[0] == "syncconf":
		f.Peers = peersOf(read(args[2]))
		if f.Ghost != "" {
			f.Peers = append(f.Peers, f.Ghost)
		}
		return nil, nil, nil
	case name == "s6-svc":
		return nil, nil, nil
	case len(args) == 1 && args[0] == "--version":
		return []byte(name + " v0-test\n"), nil, nil
	}
	return nil, []byte("fake: unexpected command " + call), fmt.Errorf("exit status 127")
}

// Called reports whether a call with this prefix was made.
func (f *Fake) Called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// Reset forgets the recorded calls.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = nil
}
