package legacy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/legacy/legacytest"
)

func TestImportKeepsEveryPeerConf(t *testing.T) {
	v := legacytest.New(t, "1,laptop", "peer1", "peer_laptop")
	v.Keepalive = []string{"peer1"}
	v.Write(t)
	rep, err := Analyze(Options{Dir: v.Dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Blockers)+len(rep.Dropped) > 0 {
		t.Fatalf("blockers %v dropped %v", rep.Blockers, rep.Dropped)
	}
	st := rep.State
	if st.Subnet() != "10.20.30.0/24" || st.Get("dns.upstream") != "9.9.9.9,149.112.112.112" || st.AWG["version"] != "2.0" {
		t.Fatalf("state %v %v", st.Settings, st.AWG["version"])
	}
	if st.Peers[0].Name != "peer1" || st.Peers[1].Name != "laptop" || st.Peers[1].ID != 3 ||
		!st.Peers[0].Keepalive || st.Peers[1].Keepalive {
		t.Fatalf("peers %+v", st.Peers)
	}
	for _, vd := range rep.Verdicts {
		if vd.Result != "same" {
			t.Errorf("%s: %s %v", vd.Name, vd.Result, vd.Changed)
		}
	}
	if len(rep.Verdicts) != 2 {
		t.Fatalf("verdicts %v", rep.Verdicts)
	}
}

func TestImportNameCollisionNeedsRename(t *testing.T) {
	v := legacytest.New(t, "1,peer1", "peer1", "peer_peer1")
	v.Write(t)
	rep, _ := Analyze(Options{Dir: v.Dir})
	if len(rep.Blockers) == 0 || !strings.Contains(strings.Join(rep.Blockers, " "), "--rename") {
		t.Fatalf("collision not reported: %v", rep.Blockers)
	}
	rep, _ = Analyze(Options{Dir: v.Dir, Rename: map[string]string{"peer_peer1": "peer1b"}})
	if len(rep.Blockers) > 0 || rep.State.Peers[1].Name != "peer1b" {
		t.Fatalf("rename: %v %+v", rep.Blockers, rep.State.Peers)
	}
}

func TestImportFindsUnsupportedItems(t *testing.T) {
	v := legacytest.New(t, "2", "peer1", "peer2")
	v.ExtraUp = "PostUp = iptables -A FORWARD -p tcp --dport 22 -j DROP\n"
	v.Routes = map[string]string{"peer2": "192.168.77.0/24"}
	v.DNSExtra = "address=/router.lan/192.168.1.1\n"
	v.Write(t)
	rep, err := Analyze(Options{Dir: v.Dir})
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(rep.Dropped, "\n")
	for _, want := range []string{"custom PostUp", "site-to-site", "custom dnsmasq option"} {
		if !strings.Contains(all, want) {
			t.Errorf("not reported: %s\n%s", want, all)
		}
	}
}

func TestImportInfersVersionAndSkipsEnv(t *testing.T) {
	v := legacytest.New(t, "1", "peer1")
	v.Params = legacytest.Params("3.1")
	v.WriteParams = false
	v.Write(t)
	rep, err := Analyze(Options{Dir: v.Dir})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	if rep.State.AWG["version"] != "3.1" {
		t.Fatalf("inferred %s, want 3.1", rep.State.AWG["version"])
	}
	if !strings.Contains(strings.Join(rep.Notes, " "), "inferred 3.1") {
		t.Errorf("notes %v", rep.Notes)
	}
}

func TestClientModeVolumeIsRefused(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "wg_confs"), 0o700)
	os.WriteFile(filepath.Join(dir, "wg_confs", "wg0.conf"), []byte("[Interface]\nPrivateKey = x\n"), 0o600)
	if _, err := Analyze(Options{Dir: dir}); err == nil || !strings.Contains(err.Error(), "client-mode") {
		t.Fatalf("got %v", err)
	}
}

func TestAnalyzeRefusesBrokenVolumes(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*legacytest.Volume)
		want   string
	}{
		"no wg0.conf":  {func(v *legacytest.Volume) { v.Rm(t, "wg_confs/wg0.conf") }, "no readable wg_confs/wg0.conf"},
		"no interface": {func(v *legacytest.Volume) { v.Put(t, "wg_confs/wg0.conf", "[Peer]\nPublicKey = x\n") }, "wg0.conf has no [Interface]"},
		"bad address": {func(v *legacytest.Volume) {
			v.Edit(t, "wg_confs/wg0.conf", "Address = 10.20.30.1", "Address = 10.20.30.5")
		},
			`wg0.conf Address "10.20.30.5" is not <subnet>.1`},
	} {
		v := legacytest.New(t, "1", "peer1")
		v.Write(t)
		c.change(v)
		if _, err := Analyze(Options{Dir: v.Dir}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestAnalyzeBlockers(t *testing.T) {
	jc := regexp.MustCompile(`(?m)^Jc = .*$`)
	for name, c := range map[string]struct {
		change func(*legacytest.Volume)
		rename map[string]string
		want   string
	}{
		"server key": {change: func(v *legacytest.Volume) { v.Put(t, "server/privatekey-server", keys.NewPrivate()) },
			want: "server/privatekey-server does not match the PrivateKey in wg0.conf"},
		"bad MTU": {change: func(v *legacytest.Volume) { v.Edit(t, "wg_confs/wg0.conf", "MTU = 1280", "MTU = big") },
			want: `net.mtu: "big"`},
		"two NAT interfaces": {change: func(v *legacytest.Volume) {
			v.Edit(t, "wg_confs/wg0.conf", "PostDown = iptables -t nat -D POSTROUTING -o eth+", "PostDown = iptables -t nat -D POSTROUTING -o ens3")
		}, want: "two different MASQUERADE interfaces (eth+, ens3)"},
		"bad peer key": {change: func(v *legacytest.Volume) { v.Put(t, "peer1/privatekey-peer1", "short") },
			want: "peer1: private key: a key is 44 base64 characters"},
		"preshared key": {change: func(v *legacytest.Volume) { v.Put(t, "peer1/presharedkey-peer1", keys.NewSymmetric()) },
			want: "peer1: the preshared key does not match wg0.conf"},
		"no address": {change: func(v *legacytest.Volume) {
			v.Edit(t, "wg_confs/wg0.conf", "AllowedIPs = 10.20.30.2/32", "AllowedIPs = 10.99.0.2/32")
		},
			want: "peer1: no /32 address in 10.20.30.0/24"},
		"bad name": {rename: map[string]string{"peer1": "bad name!"},
			want: `peer1: name "bad name!" is not valid; use --rename peer1=<name>`},
		"bad AWG value": {change: func(v *legacytest.Volume) {
			p := filepath.Join(v.Dir, "wg_confs", "wg0.conf")
			b, _ := os.ReadFile(p)
			os.WriteFile(p, jc.ReplaceAll(b, []byte("Jc = 500")), 0o600)
		}, want: "Jc (500) must be 0-128"},
	} {
		v := legacytest.New(t, "1", "peer1")
		v.Write(t)
		if c.change != nil {
			c.change(v)
		}
		rep, err := Analyze(Options{Dir: v.Dir, Rename: c.rename})
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if all := strings.Join(rep.Blockers, "\n"); !strings.Contains(all, c.want) {
			t.Errorf("%s: blockers %q, want %q", name, all, c.want)
		}
		if len(rep.Verdicts) != 0 {
			t.Errorf("%s: a blocked import verified peers: %v", name, rep.Verdicts)
		}
	}
}

func TestAnalyzeDropsBrokenPeers(t *testing.T) {
	v := legacytest.New(t, "3", "peer1", "peer2", "peer3")
	v.Write(t)
	v.Put(t, "peer1/privatekey-peer1", keys.NewPrivate())
	v.Rm(t, "peer2/privatekey-peer2")
	v.Rm(t, "peer3")
	rep, err := Analyze(Options{Dir: v.Dir})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	all := strings.Join(rep.Dropped, "\n")
	for _, want := range []string{
		"peer1 is not in wg0.conf (its key does not match any [Peer])",
		"peer2 has no privatekey-peer2",
		"peer3 is listed in PEERS but has no directory",
		"with no peer directory; it cannot be exported",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("not dropped: %s\n%s", want, all)
		}
	}
	if n := strings.Count(all, "with no peer directory"); n != 3 {
		t.Errorf("%d server peers without a directory, want 3", n)
	}
	// Without a peer conf, the peer MTU comes from wg0.conf.
	if len(rep.State.Peers) != 0 || rep.State.Get("net.peer_mtu") != "1280" {
		t.Errorf("peers %v, peer MTU %q", rep.State.Peers, rep.State.Get("net.peer_mtu"))
	}
}

func TestAnalyzeDefaultsWithoutPeerConf(t *testing.T) {
	v := legacytest.New(t, "1", "peer1")
	v.Write(t)
	v.Put(t, ".donoteditthisfile", "ORIG_SERVERURL=\"vpn.example.com\"\nORIG_PEERS=\"1\"\nnot a key value line\n")
	v.Rm(t, "peer1/peer1.conf")
	v.Rm(t, "peer1/presharedkey-peer1")
	v.Rm(t, "server/privatekey-server")
	v.Rm(t, "dnsmasq/dnsmasq.conf")
	v.Rm(t, "dnsmasq/.donoteditthisfile")
	v.Edit(t, "wg_confs/wg0.conf", "MTU = 1280\n", "")
	rep, err := Analyze(Options{Dir: v.Dir})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	st := rep.State
	for k, want := range map[string]string{"server.endpoint_port": "51820", "peer.default_allowed_ips": "0.0.0.0/0, ::/0",
		"net.mtu": "none", "net.peer_mtu": "none", "dns.upstream": "1.1.1.1,1.0.0.1"} {
		if st.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, st.Get(k), want)
		}
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "peer1 has no preshared key") {
		t.Errorf("notes %v", rep.Notes)
	}
	if len(rep.Verdicts) != 1 || rep.Verdicts[0].Result != "unverifiable" {
		t.Errorf("verdicts %v", rep.Verdicts)
	}
}

func TestAnalyzeWithoutSavedState(t *testing.T) {
	v := legacytest.New(t, "1,laptop", "peer1", "peer_laptop")
	v.Write(t)
	v.Rm(t, ".donoteditthisfile")
	v.Rm(t, "dnsmasq/dnsmasq.conf")
	v.Put(t, "dnsmasq/.donoteditthisfile", "ORIG_DNS_UPSTREAM=\"8.8.8.8\"\n")
	v.Put(t, "peer_notes.txt", "a file, not a peer directory")
	v.Edit(t, "peer1/peer1.conf", "DNS = 10.20.30.1", "DNS = 9.9.9.9")
	rep, err := Analyze(Options{Dir: v.Dir, Rename: map[string]string{"laptop": "phone"}})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "no .donoteditthisfile") {
		t.Errorf("notes %v", rep.Notes)
	}
	st := rep.State
	if len(st.Peers) != 2 || st.Peers[0].Name != "peer1" || st.Peers[1].Name != "phone" {
		t.Fatalf("peers %+v", st.Peers)
	}
	if st.Get("dns.upstream") != "8.8.8.8" || st.Get("server.endpoint_host") != "vpn.example.com" {
		t.Errorf("settings %v", st.Settings)
	}
	verdicts := map[string]Verdict{}
	for _, vd := range rep.Verdicts {
		verdicts[vd.Name] = vd
	}
	if vd := verdicts["peer1"]; vd.Result != "changed" || strings.Join(vd.Changed, " ") != "Interface.dns" {
		t.Errorf("peer1: %+v", vd)
	}
	if vd := verdicts["phone"]; vd.Result != "same" {
		t.Errorf("phone: %+v", vd)
	}
}

func TestAnalyzeFirewallRules(t *testing.T) {
	v := legacytest.New(t, "1", "peer1")
	v.ExtraUp = "PostUp = ip6tables -A FORWARD -i %i -j ACCEPT; ip6tables -t nat -A POSTROUTING -o eth0 -j MASQUERADE\n"
	v.Write(t)
	rep, err := Analyze(Options{Dir: v.Dir})
	if err != nil || len(rep.Blockers)+len(rep.Dropped) > 0 {
		t.Fatalf("%v %v %v", err, rep.Blockers, rep.Dropped)
	}
	if rep.State.Get("fw.ipv6_forward_drop") != "true" || rep.State.Get("fw.nat_interface") != "eth+" {
		t.Errorf("firewall %v", rep.State.Settings)
	}

	v = legacytest.New(t, "1", "peer1")
	v.Write(t)
	v.Edit(t, "wg_confs/wg0.conf", "PostUp = iptables -t nat -A POSTROUTING -o eth+ -j MASQUERADE\n", "")
	v.Edit(t, "wg_confs/wg0.conf", "PostDown = iptables -t nat -D POSTROUTING -o eth+ -j MASQUERADE || true\n", "")
	rep, err = Analyze(Options{Dir: v.Dir})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	if rep.State.Get("fw.nat_interface") != "eth+" || !strings.Contains(strings.Join(rep.Notes, "\n"), "no MASQUERADE rule found") {
		t.Errorf("nat %q, notes %v", rep.State.Get("fw.nat_interface"), rep.Notes)
	}
}

func TestImportInfersOlderVersions(t *testing.T) {
	for _, version := range []string{"3.0", "2.0", "1.5"} {
		v := legacytest.New(t, "1", "peer1")
		v.Params = legacytest.Params(version)
		v.WriteParams = false
		v.Write(t)
		rep, err := Analyze(Options{Dir: v.Dir})
		if err != nil || len(rep.Blockers) > 0 {
			t.Fatalf("%s: %v %v", version, err, rep.Blockers)
		}
		if got := rep.State.AWG["version"]; got != version {
			t.Errorf("inferred %s, want %s", got, version)
		}
		if len(rep.Verdicts) != 1 || rep.Verdicts[0].Result != "same" {
			t.Errorf("%s: verdicts %+v", version, rep.Verdicts)
		}
	}
}

func TestAnalyzeKeepsWarnings(t *testing.T) {
	v := legacytest.New(t, "1", "peer1")
	v.Write(t)
	p := filepath.Join(v.Dir, "wg_confs", "wg0.conf")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, regexp.MustCompile(`(?m)^S4 = .*$`).ReplaceAll(b, []byte("S4 = 24")), 0o600)
	rep, err := Analyze(Options{Dir: v.Dir})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "warning: S4 (24) is above 20") {
		t.Errorf("notes %v", rep.Notes)
	}
}
