package config_test

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

func state(t *testing.T) *config.State {
	t.Helper()
	env := map[string]string{"SERVERURL": "vpn.example.com"}
	st, _, err := seed.Build(context.Background(), rand.New(rand.NewPCG(1, 2)),
		func(k string) (string, bool) { v, ok := env[k]; return v, ok }, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func addPeer(st *config.State, name string, octet int) {
	priv, pub := keys.Pair()
	st.Peers = append(st.Peers, store.Peer{ID: int64(len(st.Peers) + 1), Name: name, Octet: octet,
		PrivateKey: priv, PublicKey: pub, PresharedKey: keys.NewSymmetric(), Enabled: true})
}

func TestValidateCatchesBadPeers(t *testing.T) {
	st := state(t)
	addPeer(st, "phone", 2)
	if errs, _ := st.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	bad := st.Clone()
	_, other := keys.Pair()
	bad.Peers[0].PublicKey = other
	if err := bad.Err(); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("mismatched key pair: %v", err)
	}
	dup := st.Clone()
	addPeer(dup, "phone", 3)
	if dup.Err() == nil {
		t.Fatal("duplicate name accepted")
	}
	inj := st.Clone()
	inj.Settings["fw.nat_interface"] = "eth0;reboot"
	if inj.Err() == nil {
		t.Fatal("a hand-edited interface pattern passed validation")
	}
	sub := st.Clone()
	sub.Settings["net.subnet"] = "10.0.0.0/16"
	if sub.Err() == nil {
		t.Fatal("a /16 subnet passed validation")
	}
}

func TestFindPeer(t *testing.T) {
	st := state(t)
	addPeer(st, "phone", 2)
	addPeer(st, "123", 3)
	for sel, want := range map[string]string{"1": "phone", "#2": "123", "phone": "phone", "name:123": "123"} {
		p, err := st.FindPeer(sel)
		if err != nil || p.Name != want {
			t.Errorf("%s: %v %v", sel, p, err)
		}
	}
	if _, err := st.FindPeer("9"); err == nil {
		t.Error("missing id found")
	}
}

func TestPendingRestartIsComputed(t *testing.T) {
	st := state(t)
	active := config.ActiveOf(st)
	if p := config.PendingRestart(st, active); len(p) != 0 {
		t.Fatalf("pending %v right after start", p)
	}
	d := st.Clone()
	d.Settings["net.subnet"] = "10.99.0.0/24"
	d.Settings["peer.keepalive_seconds"] = "10" // live: never pending
	d.AWG["jc"] = "99"
	got := strings.Join(config.PendingRestart(d, active), ",")
	if got != "awg.jc,net.subnet" && !(strings.Contains(got, "net.subnet") && strings.Contains(got, "awg.jc")) {
		t.Fatalf("pending = %s", got)
	}
	if strings.Contains(got, "keepalive") {
		t.Fatalf("a live setting is pending: %s", got)
	}
}

func TestFreeOctet(t *testing.T) {
	st := state(t)
	addPeer(st, "a", 2)
	addPeer(st, "b", 4)
	if o, _ := st.FreeOctet(); o != 3 {
		t.Fatalf("free octet %d, want 3", o)
	}
}
