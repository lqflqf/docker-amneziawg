package legacy

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
)

// oldVolume writes a /config volume the way the shell release did: confs from
// its templates, keys in files, the saved state as ORIG_ lines.
type oldVolume struct {
	dir        string
	peers      string // the saved PEERS value
	ids        []string
	params     awg.Params
	writeParam bool
	extraUp    string
	routes     map[string]string // id -> extra server-side AllowedIPs
	dnsExtra   string
}

func (v *oldVolume) write(t *testing.T) {
	t.Helper()
	w := func(rel, body string) {
		p := filepath.Join(v.dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sp, spub := keys.Pair()
	w("server/privatekey-server", sp+"\n")
	w("server/publickey-server", spub+"\n")
	w(".donoteditthisfile", fmt.Sprintf("ORIG_SERVERURL=\"vpn.example.com\"\nORIG_SERVERPORT=\"51820\"\nORIG_PEERS=\"%s\"\nORIG_INTERFACE=\"10.20.30\"\nORIG_ALLOWEDIPS=\"0.0.0.0/0, ::/0\"\n", v.peers))
	if v.writeParam {
		var b strings.Builder
		for _, k := range awg.AllKeys {
			fmt.Fprintf(&b, "%s=%s\n", strings.ToUpper("AWG_"+k), v.params[k])
		}
		w("server/awg_params", b.String())
	}
	var awgLines strings.Builder
	for _, l := range awg.Lines(v.params) {
		fmt.Fprintf(&awgLines, "%s = %s\n", l.Key, l.Value)
	}
	srv := "[Interface]\nAddress = 10.20.30.1\nListenPort = 51820\nPrivateKey = " + sp + "\nMTU = 1280\n" +
		"PostUp = ip6tables -I FORWARD -i %i -j DROP\nPostUp = ip6tables -I FORWARD -o %i -j DROP\n" +
		"PostUp = iptables -A FORWARD -i %i -j ACCEPT\nPostUp = iptables -A FORWARD -o %i -j ACCEPT\n" +
		"PostUp = iptables -t nat -A POSTROUTING -o eth+ -j MASQUERADE\n" + v.extraUp +
		"PostDown = iptables -t nat -D POSTROUTING -o eth+ -j MASQUERADE || true\n" +
		"PostDown = iptables -D FORWARD -o %i -j ACCEPT || true\nPostDown = iptables -D FORWARD -i %i -j ACCEPT || true\n" +
		"PostDown = ip6tables -D FORWARD -o %i -j DROP || true\nPostDown = ip6tables -D FORWARD -i %i -j DROP || true\n" +
		awgLines.String() + "\n"
	for i, id := range v.ids {
		pp, ppub := keys.Pair()
		psk := keys.NewSymmetric()
		ip := fmt.Sprintf("10.20.30.%d", i+2)
		w(id+"/privatekey-"+id, pp+"\n")
		w(id+"/publickey-"+id, ppub+"\n")
		w(id+"/presharedkey-"+id, psk+"\n")
		w(id+"/"+id+".conf", "[Interface]\nAddress = "+ip+"\nPrivateKey = "+pp+"\nMTU = 1280\nDNS = 10.20.30.1\n"+
			awgLines.String()+"\n[Peer]\nPublicKey = "+spub+"\nPresharedKey = "+psk+
			"\nEndpoint = vpn.example.com:51820\nAllowedIPs = 0.0.0.0/0, ::/0\n")
		allowed := ip + "/32"
		if r := v.routes[id]; r != "" {
			allowed += "," + r
		}
		srv += "[Peer]\n# " + id + "\nPublicKey = " + ppub + "\nPresharedKey = " + psk +
			"\nAllowedIPs = " + allowed + "\nPersistentKeepalive = 25\n\n"
	}
	w("wg_confs/wg0.conf", srv)
	w("dnsmasq/dnsmasq.conf", "domain-needed\nserver=9.9.9.9\nserver=149.112.112.112\n")
	w("templates/dnsmasq.conf", "# comment\ndomain-needed\nbogus-priv\nno-hosts\ncache-size=1000\n${DNS_SERVER_LINES}\n"+v.dnsExtra)
}

func newOld(t *testing.T, peers string, ids ...string) *oldVolume {
	return &oldVolume{dir: t.TempDir(), peers: peers, ids: ids, writeParam: true,
		params: awg.Generate(rand.New(rand.NewPCG(1, 1)), "2.0")}
}

func TestImportKeepsEveryPeerConf(t *testing.T) {
	v := newOld(t, "1,laptop", "peer1", "peer_laptop")
	v.write(t)
	rep, err := Analyze(Options{Dir: v.dir})
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
	if st.Peers[0].Name != "peer1" || st.Peers[1].Name != "laptop" || st.Peers[1].ID != 3 || !st.Peers[0].Keepalive {
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
	v := newOld(t, "1,peer1", "peer1", "peer_peer1")
	v.write(t)
	rep, _ := Analyze(Options{Dir: v.dir})
	if len(rep.Blockers) == 0 || !strings.Contains(strings.Join(rep.Blockers, " "), "--rename") {
		t.Fatalf("collision not reported: %v", rep.Blockers)
	}
	rep, _ = Analyze(Options{Dir: v.dir, Rename: map[string]string{"peer_peer1": "peer1b"}})
	if len(rep.Blockers) > 0 || rep.State.Peers[1].Name != "peer1b" {
		t.Fatalf("rename: %v %+v", rep.Blockers, rep.State.Peers)
	}
}

func TestImportFindsUnsupportedItems(t *testing.T) {
	v := newOld(t, "2", "peer1", "peer2")
	v.extraUp = "PostUp = iptables -A FORWARD -p tcp --dport 22 -j DROP\n"
	v.routes = map[string]string{"peer2": "192.168.77.0/24"}
	v.dnsExtra = "address=/router.lan/192.168.1.1\n"
	v.write(t)
	rep, err := Analyze(Options{Dir: v.dir})
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
	v := newOld(t, "1", "peer1")
	v.params = awg.Generate(rand.New(rand.NewPCG(2, 2)), "3.1")
	v.writeParam = false
	v.write(t)
	rep, err := Analyze(Options{Dir: v.dir})
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

func (v *oldVolume) edit(t *testing.T, rel, old, repl string) {
	t.Helper()
	p := filepath.Join(v.dir, rel)
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), old) {
		t.Fatalf("%s: %q not found (%v)", rel, old, err)
	}
	os.WriteFile(p, []byte(strings.Replace(string(b), old, repl, 1)), 0o600)
}

func (v *oldVolume) put(rel, body string) {
	os.WriteFile(filepath.Join(v.dir, rel), []byte(body), 0o600)
}

func (v *oldVolume) rm(rel string) { os.RemoveAll(filepath.Join(v.dir, rel)) }

func TestAnalyzeRefusesBrokenVolumes(t *testing.T) {
	for name, c := range map[string]struct {
		change func(*oldVolume)
		want   string
	}{
		"no wg0.conf":  {func(v *oldVolume) { v.rm("wg_confs/wg0.conf") }, "no readable wg_confs/wg0.conf"},
		"no interface": {func(v *oldVolume) { v.put("wg_confs/wg0.conf", "[Peer]\nPublicKey = x\n") }, "wg0.conf has no [Interface]"},
		"bad address": {func(v *oldVolume) { v.edit(t, "wg_confs/wg0.conf", "Address = 10.20.30.1", "Address = 10.20.30.5") },
			`wg0.conf Address "10.20.30.5" is not <subnet>.1`},
	} {
		v := newOld(t, "1", "peer1")
		v.write(t)
		c.change(v)
		if _, err := Analyze(Options{Dir: v.dir}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestAnalyzeBlockers(t *testing.T) {
	jc := regexp.MustCompile(`(?m)^Jc = .*$`)
	for name, c := range map[string]struct {
		change func(*oldVolume)
		rename map[string]string
		want   string
	}{
		"server key": {change: func(v *oldVolume) { v.put("server/privatekey-server", keys.NewPrivate()) },
			want: "server/privatekey-server does not match the PrivateKey in wg0.conf"},
		"bad MTU": {change: func(v *oldVolume) { v.edit(t, "wg_confs/wg0.conf", "MTU = 1280", "MTU = big") },
			want: `net.mtu: "big"`},
		"two NAT interfaces": {change: func(v *oldVolume) {
			v.edit(t, "wg_confs/wg0.conf", "PostDown = iptables -t nat -D POSTROUTING -o eth+", "PostDown = iptables -t nat -D POSTROUTING -o ens3")
		}, want: "two different MASQUERADE interfaces (eth+, ens3)"},
		"bad peer key": {change: func(v *oldVolume) { v.put("peer1/privatekey-peer1", "short") },
			want: "peer1: private key: a key is 44 base64 characters"},
		"preshared key": {change: func(v *oldVolume) { v.put("peer1/presharedkey-peer1", keys.NewSymmetric()) },
			want: "peer1: the preshared key does not match wg0.conf"},
		"no address": {change: func(v *oldVolume) {
			v.edit(t, "wg_confs/wg0.conf", "AllowedIPs = 10.20.30.2/32", "AllowedIPs = 10.99.0.2/32")
		},
			want: "peer1: no /32 address in 10.20.30.0/24"},
		"bad name": {rename: map[string]string{"peer1": "bad name!"},
			want: `peer1: name "bad name!" is not valid; use --rename peer1=<name>`},
		"bad AWG value": {change: func(v *oldVolume) {
			p := filepath.Join(v.dir, "wg_confs", "wg0.conf")
			b, _ := os.ReadFile(p)
			os.WriteFile(p, jc.ReplaceAll(b, []byte("Jc = 500")), 0o600)
		}, want: "Jc (500) must be 0-128"},
	} {
		v := newOld(t, "1", "peer1")
		v.write(t)
		if c.change != nil {
			c.change(v)
		}
		rep, err := Analyze(Options{Dir: v.dir, Rename: c.rename})
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
	v := newOld(t, "3", "peer1", "peer2", "peer3")
	v.write(t)
	v.put("peer1/privatekey-peer1", keys.NewPrivate())
	v.rm("peer2/privatekey-peer2")
	v.rm("peer3")
	rep, err := Analyze(Options{Dir: v.dir})
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
	v := newOld(t, "1", "peer1")
	v.write(t)
	v.put(".donoteditthisfile", "ORIG_SERVERURL=\"vpn.example.com\"\nORIG_PEERS=\"1\"\nnot a key value line\n")
	v.rm("peer1/peer1.conf")
	v.rm("peer1/presharedkey-peer1")
	v.rm("server/privatekey-server")
	v.rm("dnsmasq/dnsmasq.conf")
	v.edit(t, "wg_confs/wg0.conf", "MTU = 1280\n", "")
	rep, err := Analyze(Options{Dir: v.dir})
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
	v := newOld(t, "1,laptop", "peer1", "peer_laptop")
	v.write(t)
	v.rm(".donoteditthisfile")
	v.rm("dnsmasq/dnsmasq.conf")
	v.put("dnsmasq/.donoteditthisfile", "ORIG_DNS_UPSTREAM=\"8.8.8.8\"\n")
	v.put("peer_notes.txt", "a file, not a peer directory")
	v.edit(t, "peer1/peer1.conf", "DNS = 10.20.30.1", "DNS = 9.9.9.9")
	rep, err := Analyze(Options{Dir: v.dir, Rename: map[string]string{"laptop": "phone"}})
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
	v := newOld(t, "1", "peer1")
	v.extraUp = "PostUp = ip6tables -A FORWARD -i %i -j ACCEPT; ip6tables -t nat -A POSTROUTING -o eth0 -j MASQUERADE\n"
	v.write(t)
	rep, err := Analyze(Options{Dir: v.dir})
	if err != nil || len(rep.Blockers)+len(rep.Dropped) > 0 {
		t.Fatalf("%v %v %v", err, rep.Blockers, rep.Dropped)
	}
	if rep.State.Get("fw.ipv6_forward_drop") != "true" || rep.State.Get("fw.nat_interface") != "eth+" {
		t.Errorf("firewall %v", rep.State.Settings)
	}

	v = newOld(t, "1", "peer1")
	v.write(t)
	v.edit(t, "wg_confs/wg0.conf", "PostUp = iptables -t nat -A POSTROUTING -o eth+ -j MASQUERADE\n", "")
	v.edit(t, "wg_confs/wg0.conf", "PostDown = iptables -t nat -D POSTROUTING -o eth+ -j MASQUERADE || true\n", "")
	rep, err = Analyze(Options{Dir: v.dir})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	if rep.State.Get("fw.nat_interface") != "eth+" || !strings.Contains(strings.Join(rep.Notes, "\n"), "no MASQUERADE rule found") {
		t.Errorf("nat %q, notes %v", rep.State.Get("fw.nat_interface"), rep.Notes)
	}
}

func TestImportInfersOlderVersions(t *testing.T) {
	for _, version := range []string{"3.0", "2.0", "1.5"} {
		v := newOld(t, "1", "peer1")
		v.params = awg.Generate(rand.New(rand.NewPCG(3, 3)), version)
		v.writeParam = false
		v.write(t)
		rep, err := Analyze(Options{Dir: v.dir})
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
	v := newOld(t, "1", "peer1")
	v.write(t)
	p := filepath.Join(v.dir, "wg_confs", "wg0.conf")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, regexp.MustCompile(`(?m)^S4 = .*$`).ReplaceAll(b, []byte("S4 = 24")), 0o600)
	rep, err := Analyze(Options{Dir: v.dir})
	if err != nil || len(rep.Blockers) > 0 {
		t.Fatalf("%v %v", err, rep.Blockers)
	}
	if !strings.Contains(strings.Join(rep.Notes, "\n"), "warning: S4 (24) is above 20") {
		t.Errorf("notes %v", rep.Notes)
	}
}
