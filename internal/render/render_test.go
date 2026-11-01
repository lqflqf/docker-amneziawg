package render_test

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

func state(t *testing.T) *config.State {
	t.Helper()
	env := map[string]string{"SERVERURL": "vpn.example.com", "INTERNAL_SUBNET": "10.5.5.0"}
	st, _, err := seed.Build(context.Background(), rand.New(rand.NewPCG(3, 4)),
		func(k string) (string, bool) { v, ok := env[k]; return v, ok }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range []string{"phone", "laptop"} {
		priv, pub := keys.Pair()
		st.Peers = append(st.Peers, store.Peer{ID: int64(i + 1), Name: n, Octet: i + 2, PrivateKey: priv,
			PublicKey: pub, PresharedKey: keys.NewSymmetric(), Keepalive: i == 0, Enabled: true})
	}
	return st
}

func TestServerConf(t *testing.T) {
	st := state(t)
	st.Peers[1].Enabled = false
	f, err := render.Server(st, config.ActiveOf(st))
	if err != nil {
		t.Fatal(err)
	}
	out := f.String()
	for _, want := range []string{
		"Address = 10.5.5.1/24", "ListenPort = 51820", "MTU = 1280",
		"PostUp = iptables -t nat -A POSTROUTING -o eth+ -j MASQUERADE",
		"PostUp = ip6tables -I FORWARD -i %i -j DROP",
		"RandomTrailers = on", "# #1 phone", "AllowedIPs = 10.5.5.2/32", "PersistentKeepalive = 25",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "laptop") || strings.Contains(out, "DNS =") {
		t.Errorf("disabled peer or DNS in the server conf:\n%s", out)
	}
	// Every AWG key stays in [Interface], above the first [Peer].
	head := out[:strings.Index(out, "[Peer]")]
	if !strings.Contains(head, "I1 = ") || !strings.Contains(head, "HeaderProtectionKey = ") {
		t.Errorf("AWG keys must be in [Interface]:\n%s", head)
	}
}

func TestServerConfUsesActiveInterface(t *testing.T) {
	st := state(t)
	active := config.ActiveOf(st)
	d := st.Clone()
	d.Settings["net.subnet"] = "10.77.0.0/24"
	d.Settings["server.private_key"] = keys.NewPrivate()
	d.AWG["jc"] = "99"
	f, _ := render.Server(d, active)
	out := f.String()
	if !strings.Contains(out, "Address = 10.5.5.1/24") || !strings.Contains(out, "AllowedIPs = 10.5.5.2/32") {
		t.Errorf("a pending subnet leaked into the live conf:\n%s", out)
	}
	if !strings.Contains(out, "PrivateKey = "+active["server.private_key"]) || strings.Contains(out, "Jc = 99") {
		t.Errorf("a pending key or AWG value leaked into the live conf")
	}
}

func TestPeerConf(t *testing.T) {
	st := state(t)
	f, err := render.Peer(st, st.Peers[0])
	if err != nil {
		t.Fatal(err)
	}
	out := f.String()
	iface := out[:strings.Index(out, "[Peer]")]
	for _, want := range []string{"Address = 10.5.5.2/32", "DNS = 10.5.5.1", "MTU = 1280", "I1 = <b 0xc3>"} {
		if !strings.Contains(iface, want) {
			t.Errorf("[Interface] lacks %q:\n%s", want, out)
		}
	}
	for _, want := range []string{"Endpoint = vpn.example.com:51820", "AllowedIPs = 0.0.0.0/0, ::/0", "PresharedKey = "} {
		if !strings.Contains(out[len(iface):], want) {
			t.Errorf("[Peer] lacks %q", want)
		}
	}
}

func TestZeroPeersAndDnsmasq(t *testing.T) {
	st := state(t)
	st.Peers = nil
	f, _ := render.Server(st, config.ActiveOf(st))
	if strings.Contains(f.String(), "[Peer]") {
		t.Fatal("peer section with zero peers")
	}
	d := render.Dnsmasq("1.1.1.1,2606:4700::1111#53")
	for _, want := range []string{"interface=wg0", "bind-dynamic", "user=dnsmasq", "server=1.1.1.1\n", "server=2606:4700::1111#53\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("dnsmasq conf lacks %q", want)
		}
	}
	if strings.Contains(d, "listen-address") {
		t.Error("listen-address answers on every interface")
	}
}
