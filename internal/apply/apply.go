// Package apply moves the desired state to the running system: it keeps the
// active snapshot and the apply state in /run, checks confs with awg and
// dnsmasq, and changes a running wg0 with awg syncconf.
package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
)

// ErrNotUp means wg0 is not running; the change is saved and is applied when
// the tunnel service brings wg0 up.
var ErrNotUp = errors.New("wg0 is not up")

// Env is what apply needs from the outside.
type Env struct {
	P sys.Paths
	R sys.Runner
}

// State is /run/amneziawg/state.json. It holds no secrets.
type State struct {
	Committed      int64    `json:"committed_generation"`
	Applied        int64    `json:"applied_generation"`
	ApplyError     string   `json:"apply_error,omitempty"`
	PendingSince   string   `json:"pending_since,omitempty"`
	PendingRestart []string `json:"pending_restart,omitempty"`
}

// Pending reports whether a saved generation is not applied yet.
func (s State) Pending() bool { return s.Applied < s.Committed }

// ReadState reads state.json.
func (e Env) ReadState() (State, error) {
	var s State
	b, err := os.ReadFile(e.P.StateJSON())
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// WriteState writes state.json (mode 644).
func (e Env) WriteState(s State) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	return sys.WriteAtomic(e.P.StateJSON(), append(b, '\n'), 0o644)
}

// ReadActive reads the active snapshot that init wrote.
func (e Env) ReadActive() (config.Active, error) {
	b, err := os.ReadFile(e.P.ActiveJSON())
	if err != nil {
		return nil, fmt.Errorf("no active state (%w); has awgctl init run?", err)
	}
	a := config.Active{}
	return a, json.Unmarshal(b, &a)
}

// WriteActive writes the active snapshot (mode 600: it holds the server key).
func (e Env) WriteActive(a config.Active) error {
	b, _ := json.MarshalIndent(a, "", "  ")
	return sys.WriteAtomic(e.P.ActiveJSON(), append(b, '\n'), 0o600)
}

func (e Env) run(ctx context.Context, name string, args ...string) (string, error) {
	out, errb, err := e.R.Run(ctx, name, args, nil, nil)
	if err != nil {
		msg := strings.TrimSpace(string(errb) + " " + string(out))
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, msg)
	}
	return string(out), nil
}

const parseCheckIface = "awgparsecheck"

var endpointRe = regexp.MustCompile(`(?m)^(\s*Endpoint\s*=).*$`)

// ParseCheck parses a conf with awg itself, as tunnel start would. awg-quick
// strip drops the awg-quick keys; awg setconf parses the rest and only then
// tries to apply it to an interface that does not exist, which fails with
// "Unable to modify interface". Any other failure is a parse error.
func (e Env) ParseCheck(ctx context.Context, text, what string) error {
	if _, err := net.InterfaceByName(parseCheckIface); err == nil {
		return fmt.Errorf("an interface named %s exists; remove it so that confs can be checked", parseCheckIface)
	}
	dir, err := os.MkdirTemp("", "awgcheck")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	conf := filepath.Join(dir, parseCheckIface+".conf")
	if err := os.WriteFile(conf, []byte(text), 0o600); err != nil {
		return err
	}
	stripped, errb, err := e.R.Run(ctx, "awg-quick", []string{"strip", conf}, nil, nil)
	if err != nil {
		return fmt.Errorf("%s: awg-quick strip: %s", what, strings.TrimSpace(string(errb)))
	}
	// setconf resolves host names; whether the endpoint resolves now is not
	// the question.
	stripped = endpointRe.ReplaceAll(stripped, []byte("$1 127.0.0.1:51820"))
	sf := filepath.Join(dir, "stripped")
	if err := os.WriteFile(sf, stripped, 0o600); err != nil {
		return err
	}
	out, errb, _ := e.R.Run(ctx, "awg", []string{"setconf", parseCheckIface, sf}, nil, []string{"WG_ENDPOINT_RESOLUTION_RETRIES=0"})
	msg := string(out) + string(errb)
	if strings.Contains(msg, "Unable to modify interface") {
		return nil
	}
	return fmt.Errorf("%s is rejected by awg: %s", what, strings.Join(strings.Fields(msg), " "))
}

// DnsmasqTest checks a dnsmasq conf with dnsmasq --test.
func (e Env) DnsmasqTest(ctx context.Context, text string) error {
	f, err := os.CreateTemp("", "dnsmasq-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	if _, err := e.run(ctx, "dnsmasq", "--test", "-C", f.Name()); err != nil {
		return fmt.Errorf("dnsmasq rejects the config: %w", err)
	}
	return nil
}

// Check validates a desired state completely before it is committed: every
// rule, then the server conf (with the desired interface section, so pending
// restart values are checked too), one peer export and the dnsmasq conf with
// the real parsers.
func (e Env) Check(ctx context.Context, d *config.State) error {
	if errs, _ := d.Validate(); len(errs) > 0 {
		return errors.Join(errs...)
	}
	srv, err := render.Server(d, config.ActiveOf(d))
	if err != nil {
		return err
	}
	if err := e.ParseCheck(ctx, srv.String(), "the server conf"); err != nil {
		return err
	}
	// Peer confs differ only in keys and address; one shows that the shape
	// parses. With no peers, check a throwaway one.
	sample := store.Peer{ID: 0, Name: "check", Octet: 254, Enabled: true}
	if len(d.Peers) > 0 {
		sample = d.Peers[0]
	} else {
		sample.PrivateKey, sample.PublicKey = keys.Pair()
		sample.PresharedKey = keys.NewSymmetric()
	}
	pc, err := render.Peer(d, sample)
	if err != nil {
		return err
	}
	if err := e.ParseCheck(ctx, pc.String(), "the peer conf"); err != nil {
		return err
	}
	return e.DnsmasqTest(ctx, render.Dnsmasq(d.Get("dns.upstream")))
}

// Up reports whether wg0 is up: the tunnel service has finished its first
// attempt and the interface exists.
func (e Env) Up(ctx context.Context) bool {
	if _, err := os.Stat(e.P.Settled()); err != nil {
		return false
	}
	_, err := e.run(ctx, "awg", "show", render.Interface)
	return err == nil
}

// WriteRuntime writes wg0.conf (active interface + desired peers) and the
// dnsmasq conf. It reports whether the dnsmasq conf changed.
func (e Env) WriteRuntime(d *config.State, a config.Active) (dnsChanged bool, err error) {
	f, err := render.Server(d, a)
	if err != nil {
		return false, err
	}
	if err := sys.WriteAtomic(e.P.WG0(), []byte(f.String()), 0o600); err != nil {
		return false, err
	}
	dns := render.Dnsmasq(d.Get("dns.upstream"))
	old, _ := os.ReadFile(e.P.DNSConf())
	if string(old) != dns {
		if err := sys.WriteAtomic(e.P.DNSConf(), []byte(dns), 0o644); err != nil {
			return false, err
		}
		dnsChanged = true
	}
	return dnsChanged, nil
}

// Live brings the running system to the desired state without touching the
// interface-level values: it rewrites wg0.conf with the active [Interface],
// runs awg syncconf and checks the peer list that wg0 reports. A changed
// dnsmasq conf restarts dnsmasq.
func (e Env) Live(ctx context.Context, d *config.State) error {
	a, err := e.ReadActive()
	if err != nil {
		return err
	}
	dnsChanged, err := e.WriteRuntime(d, a)
	if err != nil {
		return err
	}
	if dnsChanged {
		if _, err := os.Stat(e.P.S6DNS()); err == nil {
			if _, err := e.run(ctx, "s6-svc", "-r", e.P.S6DNS()); err != nil {
				return err
			}
		}
	}
	if !e.Up(ctx) {
		return ErrNotUp
	}
	stripped, err := e.run(ctx, "awg-quick", "strip", e.P.WG0())
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "wg0-stripped-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.WriteString(stripped)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if _, err := e.run(ctx, "awg", "syncconf", render.Interface, tmp.Name()); err != nil {
		return err
	}
	return e.Verify(ctx, d)
}

// Verify checks that wg0 has exactly the enabled peers. A removed peer that is
// still present is an error: its keys would still work.
func (e Env) Verify(ctx context.Context, d *config.State) error {
	out, err := e.run(ctx, "awg", "show", render.Interface, "peers")
	if err != nil {
		return err
	}
	have := strings.Fields(out)
	var want []string
	for _, p := range d.Peers {
		if p.Enabled {
			want = append(want, p.PublicKey)
		}
	}
	sort.Strings(have)
	sort.Strings(want)
	if strings.Join(have, " ") != strings.Join(want, " ") {
		return fmt.Errorf("wg0 has %d peers after the update, expected %d", len(have), len(want))
	}
	return nil
}

// Record updates state.json after an apply attempt for generation gen.
func (e Env) Record(gen int64, applyErr error, pendingRestart []string) error {
	s, _ := e.ReadState()
	if gen > s.Committed {
		s.Committed = gen
	}
	s.PendingRestart = pendingRestart
	if applyErr == nil {
		s.Applied = s.Committed
		s.ApplyError = ""
		s.PendingSince = ""
	} else {
		if errors.Is(applyErr, ErrNotUp) {
			s.ApplyError = ""
		} else {
			s.ApplyError = applyErr.Error()
		}
		if s.PendingSince == "" {
			s.PendingSince = time.Now().UTC().Format(time.RFC3339)
		}
	}
	return e.WriteState(s)
}
