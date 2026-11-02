package apply_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/apply"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
	"github.com/lqflqf/docker-amneziawg/internal/sys/systest"
)

var ctx = context.Background()

func state(t *testing.T, peers int) *config.State {
	t.Helper()
	env := map[string]string{"SERVERURL": "vpn.example.com", "INTERNAL_SUBNET": "10.5.5.0"}
	st, _, err := seed.Build(ctx, rand.New(rand.NewPCG(3, 4)),
		func(k string) (string, bool) { v, ok := env[k]; return v, ok }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range peers {
		priv, pub := keys.Pair()
		st.Peers = append(st.Peers, store.Peer{ID: int64(i + 1), Name: "peer" + string(rune('a'+i)), Octet: i + 2,
			PrivateKey: priv, PublicKey: pub, PresharedKey: keys.NewSymmetric(), Enabled: true})
	}
	return st
}

func newEnv(t *testing.T) (apply.Env, *systest.Fake) {
	t.Helper()
	f := systest.New()
	return apply.Env{P: sys.Paths{Config: t.TempDir(), Run: t.TempDir()}, R: f}, f
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// up makes the env look like a running tunnel with an active snapshot.
func up(t *testing.T, e apply.Env, f *systest.Fake, d *config.State) {
	t.Helper()
	if err := e.WriteActive(config.ActiveOf(d)); err != nil {
		t.Fatal(err)
	}
	touch(t, e.P.Settled())
	f.Up = true
}

func TestStateFile(t *testing.T) {
	e, _ := newEnv(t)
	if _, err := e.ReadState(); err == nil {
		t.Error("a missing state.json must fail")
	}
	want := apply.State{Committed: 3, Applied: 2, ApplyError: "x", PendingSince: "t", PendingRestart: []string{"net.mtu"}}
	if err := e.WriteState(want); err != nil {
		t.Fatal(err)
	}
	got, err := e.ReadState()
	if err != nil || !reflect.DeepEqual(got, want) || !got.Pending() {
		t.Errorf("got %+v, %v", got, err)
	}
	if fi, _ := os.Stat(e.P.StateJSON()); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v", fi.Mode())
	}
}

func TestRecord(t *testing.T) {
	e, _ := newEnv(t)
	read := func() apply.State {
		t.Helper()
		s, err := e.ReadState()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if err := e.Record(1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := read(); s.Committed != 1 || s.Applied != 1 || s.Pending() {
		t.Errorf("applied: %+v", s)
	}

	if err := e.Record(2, apply.ErrNotUp, []string{"net.mtu"}); err != nil {
		t.Fatal(err)
	}
	s := read()
	if s.Committed != 2 || s.Applied != 1 || s.ApplyError != "" || s.PendingSince == "" ||
		!reflect.DeepEqual(s.PendingRestart, []string{"net.mtu"}) {
		t.Errorf("not up: %+v", s)
	}
	if _, err := time.Parse(time.RFC3339, s.PendingSince); err != nil {
		t.Errorf("PendingSince: %v", err)
	}

	// A later failure keeps the first PendingSince and records the error.
	s.PendingSince = "2000-01-01T00:00:00Z"
	if err := e.WriteState(s); err != nil {
		t.Fatal(err)
	}
	if err := e.Record(1, errors.New("syncconf failed"), nil); err != nil {
		t.Fatal(err)
	}
	s = read()
	if s.Committed != 2 || s.ApplyError != "syncconf failed" || s.PendingSince != "2000-01-01T00:00:00Z" || s.PendingRestart != nil {
		t.Errorf("failed: %+v", s)
	}

	if err := e.Record(2, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s := read(); s.Pending() || s.ApplyError != "" || s.PendingSince != "" {
		t.Errorf("cleared: %+v", s)
	}
}

func TestActiveFile(t *testing.T) {
	e, _ := newEnv(t)
	if _, err := e.ReadActive(); err == nil || !strings.Contains(err.Error(), "awgctl init") {
		t.Errorf("missing active.json: %v", err)
	}
	want := config.ActiveOf(state(t, 0))
	if err := e.WriteActive(want); err != nil {
		t.Fatal(err)
	}
	got, err := e.ReadActive()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, %v", got, err)
	}
	if fi, _ := os.Stat(e.P.ActiveJSON()); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
}

func TestParseCheck(t *testing.T) {
	e, f := newEnv(t)
	good := "[Interface]\nAddress = 10.0.0.1/24\nPrivateKey = k\n[Peer]\nEndpoint = no.such.host:1\n"
	if err := e.ParseCheck(ctx, good, "conf"); err != nil {
		t.Errorf("good conf: %v", err)
	}
	if err := e.ParseCheck(ctx, "[Interface]\nBogus = 1\n", "the x conf"); err == nil ||
		!strings.Contains(err.Error(), "the x conf is rejected by awg: Line unrecognized") {
		t.Errorf("bad conf: %v", err)
	}
	f.Fail["awg-quick strip"] = "strip failed"
	if err := e.ParseCheck(ctx, good, "conf"); err == nil || !strings.Contains(err.Error(), "strip failed") {
		t.Errorf("strip failure: %v", err)
	}
}

func TestTempDirErrors(t *testing.T) {
	e, _ := newEnv(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if err := e.ParseCheck(ctx, "", "conf"); err == nil {
		t.Error("ParseCheck without a temp dir must fail")
	}
	if err := e.DnsmasqTest(ctx, ""); err == nil {
		t.Error("DnsmasqTest without a temp dir must fail")
	}
}

func TestDnsmasqTest(t *testing.T) {
	e, _ := newEnv(t)
	if err := e.DnsmasqTest(ctx, "no-resolv\n"); err != nil {
		t.Error(err)
	}
	if err := e.DnsmasqTest(ctx, "bogus-option\n"); err == nil || !strings.Contains(err.Error(), "dnsmasq rejects the config") {
		t.Errorf("bad conf: %v", err)
	}
}

// secondSetconfFails fails the second awg setconf call (the peer conf).
type secondSetconfFails struct {
	*systest.Fake
	n int
}

func (r *secondSetconfFails) Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, []byte, error) {
	if name == "awg" && len(args) > 0 && args[0] == "setconf" {
		if r.n++; r.n == 2 {
			return nil, []byte("Line unrecognized"), errors.New("exit status 1")
		}
	}
	return r.Fake.Run(ctx, name, args, stdin, env)
}

func TestCheck(t *testing.T) {
	for _, n := range []int{0, 2} {
		e, f := newEnv(t)
		if err := e.Check(ctx, state(t, n)); err != nil {
			t.Errorf("%d peers: %v", n, err)
		}
		if !f.Called("dnsmasq --test") {
			t.Errorf("%d peers: dnsmasq --test is not called", n)
		}
	}

	e, f := newEnv(t)
	bad := state(t, 1)
	bad.Peers[0].Name = "bad name!"
	if err := e.Check(ctx, bad); err == nil || f.Called("awg") {
		t.Errorf("an invalid state must fail before awg: %v", err)
	}

	f.Fail["awg setconf"] = "Line unrecognized"
	if err := e.Check(ctx, state(t, 1)); err == nil || !strings.Contains(err.Error(), "the server conf") {
		t.Errorf("server conf: %v", err)
	}
	delete(f.Fail, "awg setconf")

	f.Fail["dnsmasq"] = "bad"
	if err := e.Check(ctx, state(t, 1)); err == nil || !strings.Contains(err.Error(), "dnsmasq") {
		t.Errorf("dnsmasq conf: %v", err)
	}

	e.R = &secondSetconfFails{Fake: systest.New()}
	if err := e.Check(ctx, state(t, 1)); err == nil || !strings.Contains(err.Error(), "the peer conf") {
		t.Errorf("peer conf: %v", err)
	}
}

func TestUp(t *testing.T) {
	e, f := newEnv(t)
	f.Up = true
	if e.Up(ctx) {
		t.Error("up without the settled marker")
	}
	touch(t, e.P.Settled())
	if !e.Up(ctx) {
		t.Error("not up with the marker and wg0")
	}
	f.Up = false
	if e.Up(ctx) {
		t.Error("up without wg0")
	}
}

func TestWriteRuntime(t *testing.T) {
	e, _ := newEnv(t)
	d := state(t, 1)
	a := config.ActiveOf(d)
	for i, want := range []bool{true, false} {
		changed, err := e.WriteRuntime(d, a)
		if err != nil || changed != want {
			t.Errorf("write %d: changed %v, err %v", i, changed, err)
		}
	}
	b, _ := os.ReadFile(e.P.WG0())
	if !strings.Contains(string(b), d.Peers[0].PublicKey) {
		t.Errorf("wg0.conf has no peer:\n%s", b)
	}
	d.Settings["dns.upstream"] = "9.9.9.9"
	if changed, _ := e.WriteRuntime(d, a); !changed {
		t.Error("a new upstream must change the dnsmasq conf")
	}
	if b, _ := os.ReadFile(e.P.DNSConf()); !strings.Contains(string(b), "9.9.9.9") {
		t.Errorf("dnsmasq conf:\n%s", b)
	}

	a["server.private_key"] = "a\nb"
	if _, err := e.WriteRuntime(d, a); err == nil {
		t.Error("a newline in a value must fail")
	}
}

func TestWriteRuntimeErrors(t *testing.T) {
	d := state(t, 0)
	a := config.ActiveOf(d)
	for _, block := range []func(sys.Paths) string{
		func(p sys.Paths) string { return p.RunDir() },
		func(p sys.Paths) string { return p.DNSDir() },
	} {
		e, _ := newEnv(t)
		touch(t, block(e.P))
		if _, err := e.WriteRuntime(d, a); err == nil {
			t.Errorf("a file at %s must fail", block(e.P))
		}
	}
}

func TestLive(t *testing.T) {
	e, f := newEnv(t)
	d := state(t, 2)
	if err := e.Live(ctx, d); err == nil || !strings.Contains(err.Error(), "no active state") {
		t.Errorf("no active state: %v", err)
	}

	if err := e.WriteActive(config.ActiveOf(d)); err != nil {
		t.Fatal(err)
	}
	if err := e.Live(ctx, d); !errors.Is(err, apply.ErrNotUp) {
		t.Errorf("down: %v", err)
	}
	if f.Called("s6-svc") {
		t.Error("s6-svc without a dnsmasq service")
	}

	up(t, e, f, d)
	d.Peers[1].Enabled = false
	d.Settings["dns.upstream"] = "9.9.9.9"
	touch(t, filepath.Join(e.P.S6DNS(), "run"))
	f.Reset()
	if err := e.Live(ctx, d); err != nil {
		t.Fatal(err)
	}
	if !f.Called("s6-svc -r "+e.P.S6DNS()) || !f.Called("awg syncconf wg0") {
		t.Errorf("calls: %v", f.Calls)
	}
	if len(f.Peers) != 1 || f.Peers[0] != d.Peers[0].PublicKey {
		t.Errorf("wg0 peers = %v", f.Peers)
	}

	f.Reset()
	if err := e.Live(ctx, d); err != nil || f.Called("s6-svc") {
		t.Errorf("no dns change: %v, calls %v", err, f.Calls)
	}
}

func TestLiveErrors(t *testing.T) {
	d := state(t, 1)
	for _, c := range []struct {
		name, fail, want string
		setup            func(*testing.T, apply.Env, *config.State)
	}{
		{name: "s6-svc", fail: "s6-svc", want: "s6-svc",
			setup: func(t *testing.T, e apply.Env, _ *config.State) { touch(t, filepath.Join(e.P.S6DNS(), "run")) }},
		{name: "strip", fail: "awg-quick strip", want: "awg-quick strip"},
		{name: "syncconf", fail: "awg syncconf", want: "awg syncconf"},
		{name: "verify", fail: "awg show wg0 peers", want: "awg show wg0 peers"},
		{name: "runtime", want: "newline", setup: func(t *testing.T, e apply.Env, d *config.State) {
			a := config.ActiveOf(d)
			a["server.private_key"] = "a\nb"
			if err := e.WriteActive(a); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "tmpdir", want: "", setup: func(t *testing.T, _ apply.Env, _ *config.State) {
			t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, f := newEnv(t)
			up(t, e, f, d)
			if c.fail != "" {
				f.Fail[c.fail] = "failed"
			}
			if c.setup != nil {
				c.setup(t, e, d)
			}
			if err := e.Live(ctx, d); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestVerifyGhost(t *testing.T) {
	e, f := newEnv(t)
	d := state(t, 1)
	up(t, e, f, d)
	f.Ghost = "removed-peer-key"
	err := e.Live(ctx, d)
	if err == nil || !strings.Contains(err.Error(), "wg0 has 2 peers after the update, expected 1") {
		t.Errorf("err = %v", err)
	}
}
