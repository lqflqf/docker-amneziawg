package legacy

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
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
	if st.Peers[0].Name != "peer1" || st.Peers[1].Name != "laptop" || st.Peers[1].Octet != 3 || !st.Peers[0].Keepalive {
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
