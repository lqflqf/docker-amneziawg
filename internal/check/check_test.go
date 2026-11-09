package check_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/check"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
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
		st.Peers = append(st.Peers, store.Peer{ID: i + 2, Name: "peer" + string(rune('a'+i)),
			PrivateKey: priv, PublicKey: pub, PresharedKey: keys.NewSymmetric(), Enabled: true})
	}
	return st
}

func newEnv(t *testing.T) (check.Env, *systest.Fake) {
	t.Helper()
	f := systest.New()
	return check.Env{R: f}, f
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
