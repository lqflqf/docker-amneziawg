//go:build integration

// Package integration runs awgctl against the real awg, awg-quick,
// amneziawg-go and dnsmasq. It runs as root inside the runtime image with
// NET_ADMIN and /dev/net/tun (.github/scripts/integration-test.sh).
package integration

import (
	"bytes"
	"context"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/check"
	"github.com/lqflqf/docker-amneziawg/internal/cli"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
)

func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestMain(m *testing.M) {
	// Without the kernel module, awg-quick needs the userspace implementation.
	if exec.Command("ip", "link", "add", "awgprobe", "type", "amneziawg").Run() == nil {
		exec.Command("ip", "link", "del", "awgprobe").Run()
	} else {
		os.Setenv("WG_QUICK_USERSPACE_IMPLEMENTATION", "amneziawg-go")
	}
	os.Exit(m.Run())
}

type box struct {
	t   *testing.T
	app *cli.App
	out *bytes.Buffer
}

func newBox(t *testing.T) *box {
	b := &box{t: t, out: &bytes.Buffer{}}
	env := map[string]string{"SERVERURL": "vpn.example.com", "INTERNAL_SUBNET": "10.66.66.0"}
	b.app = &cli.App{
		P:      sys.Paths{Config: t.TempDir(), Run: t.TempDir()},
		R:      sys.Exec{},
		Out:    b.out,
		Err:    b.out,
		Getenv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		Detect: func(context.Context) (string, error) { return "", os.ErrNotExist },
		Rand:   rand.New(rand.NewPCG(1, 2)),
		Owner:  func() (fsafe.Owner, error) { return fsafe.Owner{UID: 0, GID: 0}, nil },
	}
	t.Cleanup(func() { exec.Command("awg-quick", "down", b.app.P.WG0()).Run() })
	return b
}

func (b *box) run(want int, args ...string) string {
	b.t.Helper()
	b.out.Reset()
	if got := b.app.Main(args); got != want {
		b.t.Fatalf("awgctl %s: exit %d, want %d\n%s", strings.Join(args, " "), got, want, b.out.String())
	}
	return b.out.String()
}

// start does what a container start does: init, then svc-amneziawg.
func (b *box) start() {
	b.t.Helper()
	b.run(cli.ExitOK, "init")
	sh(b.t, "awg-quick", "up", b.app.P.WG0())
}

// restart does what docker restart does: svc-amneziawg finish, then start.
func (b *box) restart() {
	b.t.Helper()
	sh(b.t, "awg-quick", "down", b.app.P.WG0())
	b.start()
}

func wgPeers(t *testing.T) []string { return strings.Fields(sh(t, "awg", "show", "wg0", "peers")) }

func TestKeysMatchAwgPubkey(t *testing.T) {
	for i := 0; i < 5; i++ {
		priv, pub := keys.Pair()
		cmd := exec.Command("awg", "pubkey")
		cmd.Stdin = strings.NewReader(priv + "\n")
		out, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(out)) != pub {
			t.Fatalf("awg pubkey %q, ours %q (%v)", out, pub, err)
		}
	}
}

func TestRealParsersAcceptRenderAndRejectBadValues(t *testing.T) {
	b := newBox(t)
	b.run(cli.ExitOK, "init")
	e := check.Env{R: sys.Exec{}}
	srv, _ := os.ReadFile(b.app.P.WG0())
	if err := e.ParseCheck(context.Background(), string(srv)+"Bogus = 1\n", "x"); err == nil {
		t.Fatal("awg accepted an unknown key")
	}
	if err := e.DnsmasqTest(context.Background(), render.Dnsmasq("1.1.1.1")+"bogus-option-ci\n"); err == nil {
		t.Fatal("dnsmasq accepted an unknown option")
	}
	// Every version renders confs that awg parses (init and each change run
	// the parse check before the commit).
	for _, v := range []string{"1.5", "2.0", "3.0", "3.1"} {
		b.run(cli.ExitOK, "awg", "version", v, "--yes")
	}
}

func TestPeerChangesApplyAtRestart(t *testing.T) {
	b := newBox(t)
	b.start() // zero peers is a valid server
	if len(wgPeers(t)) != 0 {
		t.Fatal("peers on a new server")
	}
	b.run(cli.ExitOK, "peer", "add", "--name", "phone")
	b.run(cli.ExitOK, "peer", "add")
	if n := len(wgPeers(t)); n != 0 {
		t.Fatalf("wg0 has %d peers before the restart, want 0", n)
	}
	b.restart()
	if n := len(wgPeers(t)); n != 2 {
		t.Fatalf("wg0 has %d peers, want 2", n)
	}
	b.run(cli.ExitOK, "peer", "set", "3", "--keepalive", "off")
	b.restart()
	ka := sh(t, "awg", "show", "wg0", "persistent-keepalive")
	var vals []string
	for _, l := range strings.Split(strings.TrimSpace(ka), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			vals = append(vals, f[1])
		}
	}
	if strings.Join(vals, " ") != "25 0" && strings.Join(vals, " ") != "0 25" &&
		strings.Join(vals, " ") != "25 off" && strings.Join(vals, " ") != "off 25" {
		t.Fatalf("persistent-keepalive after off:\n%s", ka)
	}
	b.run(cli.ExitOK, "peer", "disable", "2")
	b.run(cli.ExitOK, "peer", "rotate", "3", "--yes")
	b.restart()
	st := state(t, b)
	if p := wgPeers(t); len(p) != 1 || p[0] != st.Peers[1].PublicKey {
		t.Fatalf("wg0 peers %v, want only the rotated key of peer 3", p)
	}
	b.run(cli.ExitOK, "peer", "rm", "3", "--yes")
	b.run(cli.ExitOK, "peer", "enable", "2")
	b.restart()
	if p := wgPeers(t); len(p) != 1 || p[0] != st.Peers[0].PublicKey {
		t.Fatalf("wg0 peers %v", p)
	}
	exp := b.run(cli.ExitOK, "peer", "export", "2")
	if err := (check.Env{R: sys.Exec{}}).ParseCheck(context.Background(), exp, "export"); err != nil {
		t.Fatalf("awg rejects the export: %v\n%s", err, exp)
	}
}

func TestInterfaceChangesApplyAtRestart(t *testing.T) {
	b := newBox(t)
	b.start()
	key := sh(t, "awg", "show", "wg0", "private-key")
	addr := sh(t, "ip", "-4", "-o", "addr", "show", "dev", "wg0")
	b.run(cli.ExitOK, "set", "net.subnet", "10.67.0.0/24", "--yes")
	b.run(cli.ExitOK, "server", "rotate-key", "--yes")
	b.run(cli.ExitOK, "peer", "add")
	if sh(t, "awg", "show", "wg0", "private-key") != key || sh(t, "ip", "-4", "-o", "addr", "show", "dev", "wg0") != addr {
		t.Fatal("a command changed the running interface")
	}
	b.restart()
	if sh(t, "awg", "show", "wg0", "private-key") == key {
		t.Fatal("the restart kept the old server key")
	}
	if !strings.Contains(sh(t, "ip", "-4", "-o", "addr", "show", "dev", "wg0"), "10.67.0.1/24") {
		t.Fatal("the restart kept the old address")
	}
	if !strings.Contains(sh(t, "awg", "show", "wg0", "allowed-ips"), "10.67.0.2/32") {
		t.Fatal("the new peer is not in the new subnet")
	}
}

func state(t *testing.T, b *box) *config.State {
	t.Helper()
	db, err := store.Open(b.app.P.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var st *config.State
	db.View(context.Background(), func(tx *store.Tx) error {
		st, err = config.Load(tx)
		return err
	})
	return st
}
