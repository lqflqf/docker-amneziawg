package config_test

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"strconv"
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

func addPeer(st *config.State, name string, id int) {
	priv, pub := keys.Pair()
	st.Peers = append(st.Peers, store.Peer{ID: id, Name: name,
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
	addPeer(st, "laptop", 5)
	if p, err := st.FindPeer(5); err != nil || p.Name != "laptop" {
		t.Errorf("id 5: %v %v", p, err)
	}
	if _, err := st.FindPeer(9); err == nil || err.Error() != "no peer with id 9" {
		t.Errorf("missing id: %v", err)
	}
	p, _ := st.FindPeer(2)
	p.Enabled = false
	if st.Peers[0].Enabled {
		t.Error("FindPeer does not return a pointer into the state")
	}
}

func TestParseID(t *testing.T) {
	for in, want := range map[string]int{"5": 5, "254": 254, "0": 0, "007": 7} {
		if got, ok := config.ParseID(in); !ok || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "#5", "+5", "-5", " 5", "5 ", "phone", "name:5", "0x5", "1e3", "1234567890"} {
		if got, ok := config.ParseID(in); ok {
			t.Errorf("%q accepted as id %d", in, got)
		}
	}
}

func TestPeerNames(t *testing.T) {
	for n, want := range map[string]bool{
		"phone": true, "my phone": true, "peer 5": true, "a.b_c-d": true, strings.Repeat("a", 32): true,
		"": false, " phone": false, "phone ": false, "-x": false, "a/b": false, strings.Repeat("a", 33): false,
	} {
		if got := config.ValidPeerName(n); got != want {
			t.Errorf("ValidPeerName(%q) = %v, want %v", n, got, want)
		}
	}
	if config.DefaultPeerName(7) != "peer 7" {
		t.Errorf("default name %q", config.DefaultPeerName(7))
	}
	for _, c := range []struct {
		name string
		id   int
		want bool
	}{{"peer 5", 5, true}, {"peer 5", 6, false}, {"peer 05", 5, false}, {"peer5", 6, true}, {"phone", 6, true}} {
		if got := config.NameFitsID(c.name, c.id); got != c.want {
			t.Errorf("NameFitsID(%q, %d) = %v, want %v", c.name, c.id, got, c.want)
		}
	}
}

func TestFreeIDs(t *testing.T) {
	st := state(t)
	addPeer(st, "a", 2)
	addPeer(st, "b", 4)
	if ids, err := st.FreeIDs(3); err != nil || len(ids) != 3 || ids[0] != 3 || ids[1] != 5 || ids[2] != 6 {
		t.Fatalf("free ids %v %v, want [3 5 6]", ids, err)
	}
	if _, err := st.FreeIDs(252); err == nil || err.Error() != "only 251 free address(es) in 10.13.13.0/24 (at most 253 peers)" {
		t.Fatalf("too few free ids: %v", err)
	}
}

func TestRegistryChecks(t *testing.T) {
	priv, _ := keys.Pair()
	cases := []struct {
		key, in, want, err string
	}{
		{"server.private_key", priv, priv, ""},
		{"server.private_key", "abc", "", "base64 32-byte key"},
		{"server.endpoint_host", "vpn.example.com", "vpn.example.com", ""},
		{"server.endpoint_host", "bad host!", "", "not a valid host name"},
		{"server.endpoint_port", " 443 ", "443", ""},
		{"server.endpoint_port", "0443", "", "1-65535"},
		{"net.mtu", " none ", "none", ""},
		{"net.mtu", "1500", "1500", ""},
		{"net.mtu", "575", "", "576-1500 or none"},
		{"net.peer_mtu", "1501", "", "576-1500 or none"},
		{"peer.keepalive_seconds", " 25 ", "25", ""},
		{"peer.keepalive_seconds", "0", "0", ""},
		{"peer.keepalive_seconds", "025", "", "integer 0-65535"},
		{"peer.keepalive_seconds", "65536", "", "integer 0-65535"},
		{"peer.keepalive_seconds", "x", "", "integer 0-65535"},
		{"fw.nat_interface", " eth+ ", "eth+", ""},
		{"fw.nat_interface", "eth0;reboot", "", "must match"},
		{"fw.ipv6_forward_drop", "Yes", "true", ""},
		{"fw.ipv6_forward_drop", " off ", "false", ""},
		{"fw.ipv6_forward_drop", "maybe", "", "true or false"},
		{"dns.upstream", "9.9.9.9, 1.1.1.1", "9.9.9.9,1.1.1.1", ""},
		{"dns.upstream", "not-an-ip", "", ""},
	}
	for _, c := range cases {
		s, ok := config.Lookup(c.key)
		if !ok {
			t.Fatalf("%s is not in the registry", c.key)
		}
		got, err := s.Check(c.in)
		if c.err == "" && c.want == "" {
			if err == nil {
				t.Errorf("%s=%q accepted as %q", c.key, c.in, got)
			}
			continue
		}
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s=%q: error %v, want %q", c.key, c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s=%q: %q %v, want %q", c.key, c.in, got, err, c.want)
		}
	}
	if _, ok := config.Lookup("no.such.key"); ok {
		t.Error("Lookup found an unknown key")
	}
}

func TestGetAndAddresses(t *testing.T) {
	st := &config.State{Settings: map[string]string{"net.subnet": "10.1.2.0/24"}}
	if st.Get("net.mtu") != "1280" || st.Get("no.such.key") != "" || st.Subnet() != "10.1.2.0/24" {
		t.Fatalf("Get: mtu %q, unknown %q, subnet %q", st.Get("net.mtu"), st.Get("no.such.key"), st.Subnet())
	}
	if a := config.ServerAddress(st.Subnet()); a != "10.1.2.1" {
		t.Errorf("server address %s", a)
	}
	if a := config.PeerAddress(st.Subnet(), 42); a != "10.1.2.42" {
		t.Errorf("peer address %s", a)
	}
}

func TestSaveSettingsAndLoad(t *testing.T) {
	st := state(t)
	addPeer(st, "phone", 2)
	st.Settings["net.mtu"] = "1400"
	st.AWG["i2"] = "<r 10>"
	path := filepath.Join(t.TempDir(), "amneziawg.db")
	err := store.Create(path, func(tx *store.Tx) error {
		if err := st.SaveSettings(tx); err != nil {
			return err
		}
		return tx.AddPeer(st.Peers[0])
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	// An empty AWG value and an empty setting remove their rows.
	err = db.Update(ctx, func(tx *store.Tx) error {
		ch := st.Clone()
		ch.AWG["i2"] = ""
		delete(ch.Settings, "net.mtu")
		return ch.SaveSettings(tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	var got *config.State
	if err := db.View(ctx, func(tx *store.Tx) (err error) { got, err = config.Load(tx); return err }); err != nil {
		t.Fatal(err)
	}
	if len(got.Peers) != 1 || got.Peers[0] != st.Peers[0] {
		t.Errorf("loaded peers %+v", got.Peers)
	}
	if _, ok := got.Settings["net.mtu"]; ok || got.Get("net.mtu") != "1280" {
		t.Errorf("net.mtu %q was not removed", got.Settings["net.mtu"])
	}
	if _, ok := got.AWG["i2"]; ok {
		t.Error("an empty AWG value was stored")
	}
	want := st.Clone()
	delete(want.Settings, "net.mtu")
	delete(want.AWG, "i2")
	for k, v := range want.AWG {
		if v != "" && got.AWG[k] != v {
			t.Errorf("AWG %s = %q, want %q", k, got.AWG[k], v)
		}
	}
	for k, v := range want.Settings {
		if got.Settings[k] != v {
			t.Errorf("%s = %q, want %q", k, got.Settings[k], v)
		}
	}
	if err := got.Err(); err != nil {
		t.Errorf("loaded state is not valid: %v", err)
	}

	var done *store.Tx
	db.Update(ctx, func(tx *store.Tx) error { done = tx; return nil })
	if _, err := config.Load(done); err == nil {
		t.Error("Load on a finished transaction returned nil")
	}
	if err := st.SaveSettings(done); err == nil {
		t.Error("SaveSettings on a finished transaction returned nil")
	}
}

func TestValidateReportsEachProblem(t *testing.T) {
	cases := []struct {
		name string
		edit func(*config.State)
		want string
	}{
		{"unknown setting", func(s *config.State) { s.Settings["foo.bar"] = "1" }, `unknown setting "foo.bar"`},
		{"missing key", func(s *config.State) { delete(s.Settings, "server.private_key") }, "server.private_key is not set"},
		{"not normal form", func(s *config.State) { s.Settings["net.mtu"] = " 1400" }, `net.mtu: " 1400" is not in its normal form "1400"`},
		{"bad value", func(s *config.State) { s.Settings["net.mtu"] = "99" }, "net.mtu: must be 576-1500 or none"},
		{"AWG rule", func(s *config.State) { s.AWG["jc"] = "200" }, "AWG Jc (200) must be 0-128"},
		{"bad name", func(s *config.State) { s.Peers[0].Name = "-x" }, `peer 2: name "-x" is not valid`},
		{"same name", func(s *config.State) { s.Peers[1].Name = "a" }, `peer 3: name "a" is used twice`},
		{"other default name", func(s *config.State) { s.Peers[0].Name = "peer 3" }, `peer 2: name "peer 3" is the default name of peer 3 only`},
		{"id 1", func(s *config.State) { s.Peers[0].ID = 1 }, "peer 1: the id must be 2-254"},
		{"id 255", func(s *config.State) { s.Peers[0].ID = 255 }, "peer 255: the id must be 2-254"},
		{"same id", func(s *config.State) { s.Peers[1].ID = 2 }, "peer 2: the id is used twice"},
		{"private key", func(s *config.State) { s.Peers[0].PrivateKey = "x" }, "peer 2: private key:"},
		{"same key", func(s *config.State) { s.Peers[1] = withID(s.Peers[0], "b", 3) }, "peer 3: the public key is used twice"},
		{"server key", func(s *config.State) {
			priv, _ := keys.Pair()
			s.Settings["server.private_key"] = priv
			s.Peers[0].PrivateKey = priv
			s.Peers[0].PublicKey, _ = keys.Public(priv)
		}, "peer 2: the public key is used twice"},
		{"psk", func(s *config.State) { s.Peers[0].PresharedKey = keys.NewSymmetric()[:10] }, "peer 2: the preshared key is not"},
	}
	for _, c := range cases {
		st := state(t)
		addPeer(st, "a", 2)
		addPeer(st, "b", 3)
		c.edit(st)
		err := st.Err()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", c.name, err, c.want)
		}
	}
	st := state(t)
	st.AWG["s4"] = "25"
	st.AWG["random_trailers"] = "off"
	st.AWG["s1"], st.AWG["s2"], st.AWG["s3"] = "30", "40", "25"
	st.AWG["content_padding"] = "16-32"
	if errs, warns := st.Validate(); len(errs) > 0 || len(warns) == 0 || !strings.Contains(strings.Join(warns, "\n"), "S4 (25)") {
		t.Errorf("AWG warnings are not passed on: errs %v warns %v", errs, warns)
	}
	own := state(t)
	addPeer(own, "peer 2", 2)
	if err := own.Err(); err != nil {
		t.Errorf("a peer with its own default name: %v", err)
	}
	full := state(t)
	for i := 0; i <= config.MaxPeers; i++ {
		addPeer(full, "p"+strconv.Itoa(i), 2+i%253)
	}
	if err := full.Err(); err == nil || !strings.Contains(err.Error(), "254 peers; the maximum for a /24 is 253") {
		t.Errorf("too many peers: %v", err)
	}
}

func withID(p store.Peer, name string, id int) store.Peer {
	p.Name, p.ID = name, id
	return p
}
