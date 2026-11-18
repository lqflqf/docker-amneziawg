// Package legacytest writes a /config volume in the layout of the last shell
// release (v3.1.20260812-r15), for the import-legacy tests. The templates, the
// key order and the AWG values come from that release, not from the awg or
// render packages. Thus a change in those packages cannot change the volume.
package legacytest

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
)

// The INTERFACE, SERVERURL and SERVERPORT of each volume.
const (
	Interface  = "10.20.30"
	ServerURL  = "vpn.example.com"
	ServerPort = "51820"
)

// ServerTemplate, PeerTemplate and DNSTemplate are the r15 files in
// root/defaults/.
const ServerTemplate = `[Interface]
Address = ${INTERFACE}.1
ListenPort = 51820
PrivateKey = $(cat /config/server/privatekey-server)
MTU = 1280
PostUp = ip6tables -I FORWARD -i %i -j DROP
PostUp = ip6tables -I FORWARD -o %i -j DROP
PostUp = iptables -A FORWARD -i %i -j ACCEPT
PostUp = iptables -A FORWARD -o %i -j ACCEPT
PostUp = iptables -t nat -A POSTROUTING -o eth+ -j MASQUERADE
PostDown = iptables -t nat -D POSTROUTING -o eth+ -j MASQUERADE || true
PostDown = iptables -D FORWARD -o %i -j ACCEPT || true
PostDown = iptables -D FORWARD -i %i -j ACCEPT || true
PostDown = ip6tables -D FORWARD -o %i -j DROP || true
PostDown = ip6tables -D FORWARD -i %i -j DROP || true
Jc = ${AWG_JC}
Jmin = ${AWG_JMIN}
Jmax = ${AWG_JMAX}
S1 = ${AWG_S1}
S2 = ${AWG_S2}
S3 = ${AWG_S3}
S4 = ${AWG_S4}
H1 = ${AWG_H1}
H2 = ${AWG_H2}
H3 = ${AWG_H3}
H4 = ${AWG_H4}
`

const PeerTemplate = `[Interface]
Address = ${CLIENT_IP}
PrivateKey = $(cat /config/${PEER_ID}/privatekey-${PEER_ID})
MTU = 1280
DNS = ${PEERDNS}
Jc = ${AWG_JC}
Jmin = ${AWG_JMIN}
Jmax = ${AWG_JMAX}
S1 = ${AWG_S1}
S2 = ${AWG_S2}
S3 = ${AWG_S3}
S4 = ${AWG_S4}
H1 = ${AWG_H1}
H2 = ${AWG_H2}
H3 = ${AWG_H3}
H4 = ${AWG_H4}

[Peer]
PublicKey = $(cat /config/server/publickey-server)
PresharedKey = $(cat /config/${PEER_ID}/presharedkey-${PEER_ID})
Endpoint = ${SERVERURL}:${SERVERPORT}
AllowedIPs = ${ALLOWEDIPS}
`

const DNSTemplate = `# dnsmasq settings for the peers' DNS forwarder (server mode only).
# Rendered into /config/dnsmasq/dnsmasq.conf when this template or
# DNS_UPSTREAM changes; edit this file, not the rendered one, and restart.
# Like server.conf and peer.conf it is expanded by the shell, so escape a
# literal dollar sign with a backslash.
#
# The container sets user, interface (wg0, so only peers can query) and
# no-resolv itself. interface=, listen-address= or bind-interfaces lines here
# let dnsmasq answer outside the tunnel; port= breaks DNS for peers.
domain-needed
bogus-priv
no-hosts
cache-size=1000

# One server= line per DNS_UPSTREAM entry (default 1.1.1.1, then 1.0.0.1).
${DNS_SERVER_LINES}
`

// param is one AWG value: its key in awg.Params, its r15 variable and its
// conf key. The order is the r15 AWG_PARAM_NAMES order.
type param struct{ key, env, conf string }

var params = []param{
	{"version", "AWG_VERSION", ""},
	{"jc", "AWG_JC", "Jc"}, {"jmin", "AWG_JMIN", "Jmin"}, {"jmax", "AWG_JMAX", "Jmax"},
	{"s1", "AWG_S1", "S1"}, {"s2", "AWG_S2", "S2"}, {"s3", "AWG_S3", "S3"}, {"s4", "AWG_S4", "S4"},
	{"h1", "AWG_H1", "H1"}, {"h2", "AWG_H2", "H2"}, {"h3", "AWG_H3", "H3"}, {"h4", "AWG_H4", "H4"},
	{"i1", "AWG_I1", "I1"}, {"i2", "AWG_I2", "I2"}, {"i3", "AWG_I3", "I3"}, {"i4", "AWG_I4", "I4"}, {"i5", "AWG_I5", "I5"},
	{"header_protection_key", "AWG_HEADER_PROTECTION_KEY", "HeaderProtectionKey"},
	{"content_padding", "AWG_CONTENT_PADDING", "ContentPaddingAddition"},
	{"rekey_after_time", "AWG_REKEY_AFTER_TIME", "RekeyAfterTime"},
	{"rekey_timeout", "AWG_REKEY_TIMEOUT", "RekeyTimeout"},
	{"reject_after_time", "AWG_REJECT_AFTER_TIME", "RejectAfterTime"},
	{"keepalive_timeout", "AWG_KEEPALIVE_TIMEOUT", "KeepaliveTimeout"},
	{"max_handshake_attempts", "AWG_MAX_HANDSHAKE_ATTEMPTS", "MaxHandshakeAttempts"},
	{"random_trailers", "AWG_RANDOM_TRAILERS", "RandomTrailers"},
	{"disable_cookies", "AWG_DISABLE_COOKIES", "DisableCookies"},
}

// The r15 blocks after the template: I1-I5, the 3.x set and the 3.1 switches.
var (
	signatureKeys = []string{"i1", "i2", "i3", "i4", "i5"}
	x3Keys        = []string{"header_protection_key", "content_padding", "rekey_after_time",
		"rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts"}
	switchKeys = []string{"random_trailers", "disable_cookies"}
)

// Params returns AWG values that the r15 generators can make for version.
// The values are fixed, so each test gets the same values.
func Params(version string) awg.Params {
	p := awg.Params{"version": version, "jc": "5", "jmin": "50", "jmax": "200",
		"s1": "72", "s2": "115"}
	if !awg.Has2x(version) {
		p["s3"], p["s4"] = "0", "0"
		p["h1"], p["h2"], p["h3"], p["h4"] = "1234567891", "987654321", "1876543210", "456789123"
		return p
	}
	p["s3"], p["s4"] = "30", "10"
	p["h1"], p["h2"] = "100000000-150000000", "600000000-650000000"
	p["h3"], p["h4"] = "1100000000-1150000000", "1700000000-1750000000"
	p["i1"] = "<b 0xc3><b 0x00000001><b 0x08><r 8><b 0x00><b 0x00><b 0x449e><r 4><r 1178>"
	if !awg.Is3x(version) {
		return p
	}
	p["s4"] = "16"
	p["header_protection_key"] = "++usgjUWc//U4ctKnscVvY23oQ3AU/hQ70OlamTkufM="
	p["content_padding"] = "40-90"
	p["rekey_after_time"], p["rekey_timeout"] = "110-130", "5-8"
	p["reject_after_time"], p["keepalive_timeout"] = "170-190", "10-15"
	p["max_handshake_attempts"] = "15-20"
	if awg.TrailersDefault(version) == "on" {
		p["s1"], p["s2"], p["s3"], p["s4"] = "16", "16", "16", "16"
		p["content_padding"] = "0"
		p["random_trailers"] = "on"
	}
	return p
}

// Volume is one old /config volume. New sets the defaults. Change the
// fields, then call Write.
type Volume struct {
	Dir         string
	Peers       string            // PEERS
	IDs         []string          // the peer directories, in PEERS order
	Params      awg.Params        // the AWG values, see Params
	WriteParams bool              // write server/awg_params
	Keepalive   []string          // the peer ids in PERSISTENTKEEPALIVE_PEERS
	ExtraUp     string            // PostUp lines added to templates/server.conf
	Routes      map[string]string // peer id -> SERVER_ALLOWEDIPS_PEER_<n>
	DNSUpstream string            // DNS_UPSTREAM
	DNSExtra    string            // lines added to templates/dnsmasq.conf
}

// New returns a volume in a new temporary directory, with AWG 2.0 and no
// keepalive (the r15 defaults).
func New(t *testing.T, peers string, ids ...string) *Volume {
	return &Volume{Dir: t.TempDir(), Peers: peers, IDs: ids, Params: Params("2.0"),
		WriteParams: true, DNSUpstream: "9.9.9.9,149.112.112.112"}
}

// Put writes one file of the volume.
func (v *Volume) Put(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(v.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Edit replaces the first old in one file of the volume with repl.
func (v *Volume) Edit(t *testing.T, rel, old, repl string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(v.Dir, rel))
	if err != nil || !strings.Contains(string(b), old) {
		t.Fatalf("%s: %q not found (%v)", rel, old, err)
	}
	v.Put(t, rel, strings.Replace(string(b), old, repl, 1))
}

// Rm removes one file or directory of the volume.
func (v *Volume) Rm(t *testing.T, rel string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(v.Dir, rel)); err != nil {
		t.Fatal(err)
	}
}

// Chown gives each entry of the volume to uid:gid, as PUID/PGID did.
func (v *Volume) Chown(t *testing.T, uid, gid int) {
	t.Helper()
	err := filepath.WalkDir(v.Dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(p, uid, gid)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// expand does what an r15 heredoc does with a template: it replaces each
// variable and each $(cat ...) of the template.
func expand(t *testing.T, tmpl string, vars map[string]string) string {
	t.Helper()
	var pairs []string
	for k, val := range vars {
		pairs = append(pairs, k, val)
	}
	out := strings.NewReplacer(pairs...).Replace(tmpl)
	if i := strings.IndexByte(out, '$'); i >= 0 {
		t.Fatalf("template value not set: %q", out[i:min(len(out), i+40)])
	}
	// $(cat template) removes the trailing newlines. The heredoc adds one.
	return strings.TrimRight(out, "\n") + "\n"
}

// awgBlock returns the lines that r15 adds after the template: I1-I5, then
// the 3.x set, then the 3.1 switches that are on.
func (v *Volume) awgBlock() string {
	var b strings.Builder
	for _, p := range params {
		val := v.Params[p.key]
		switch {
		case slices.Contains(signatureKeys, p.key) && val != "",
			slices.Contains(x3Keys, p.key) && awg.Is3x(v.Params["version"]),
			slices.Contains(switchKeys, p.key) && val == "on":
			b.WriteString(p.conf + " = " + val + "\n")
		}
	}
	return b.String()
}

// Write writes the volume as r15 left it after a start.
func (v *Volume) Write(t *testing.T) {
	t.Helper()
	serverTmpl := ServerTemplate
	if v.ExtraUp != "" {
		masq := "PostUp = iptables -t nat -A POSTROUTING -o eth+ -j MASQUERADE\n"
		serverTmpl = strings.Replace(serverTmpl, masq, masq+v.ExtraUp, 1)
	}
	dnsTmpl := DNSTemplate + v.DNSExtra
	v.Put(t, "templates/server.conf", serverTmpl)
	v.Put(t, "templates/peer.conf", PeerTemplate)
	v.Put(t, "templates/dnsmasq.conf", dnsTmpl)

	awgVars := map[string]string{}
	for _, p := range params {
		awgVars["${"+p.env+"}"] = v.Params[p.key]
	}
	block := v.awgBlock()

	sp, spub := keys.Pair()
	v.Put(t, "server/privatekey-server", sp+"\n")
	v.Put(t, "server/publickey-server", spub+"\n")
	vars := map[string]string{"${INTERFACE}": Interface, "$(cat /config/server/privatekey-server)": sp}
	for k, val := range awgVars {
		vars[k] = val
	}
	// The heredoc adds an empty line after the template.
	srv := expand(t, serverTmpl, vars) + "\n" + block

	peerDNS := Interface + ".1"
	allowed := "0.0.0.0/0, ::/0"
	for i, id := range v.IDs {
		pp, ppub := keys.Pair()
		psk := keys.NewSymmetric()
		ip := Interface + "." + strconv.Itoa(i+2)
		v.Put(t, id+"/privatekey-"+id, pp+"\n")
		v.Put(t, id+"/publickey-"+id, ppub+"\n")
		v.Put(t, id+"/presharedkey-"+id, psk+"\n")
		vars := map[string]string{
			"${CLIENT_IP}": ip, "${PEERDNS}": peerDNS, "${SERVERURL}": ServerURL,
			"${SERVERPORT}": ServerPort, "${ALLOWEDIPS}": allowed,
			"$(cat /config/${PEER_ID}/privatekey-${PEER_ID})":   pp,
			"$(cat /config/server/publickey-server)":            spub,
			"$(cat /config/${PEER_ID}/presharedkey-${PEER_ID})": psk,
		}
		for k, val := range awgVars {
			vars[k] = val
		}
		// r15 puts the AWG block in [Interface], before the [Peer] line.
		pc := strings.Replace(expand(t, PeerTemplate, vars), "\n[Peer]\n", "\n"+block+"[Peer]\n", 1)
		v.Put(t, id+"/"+id+".conf", pc)
		v.Put(t, id+"/"+id+".png", "\x89PNG\r\n\x1a\n")

		srv += "[Peer]\n# " + id + "\nPublicKey = " + ppub + "\nPresharedKey = " + psk + "\n"
		if r := v.Routes[id]; r != "" {
			srv += "AllowedIPs = " + ip + "/32," + r + "\n"
		} else {
			srv += "AllowedIPs = " + ip + "/32\n"
		}
		if slices.Contains(v.Keepalive, id) {
			srv += "PersistentKeepalive = 25\n"
		}
		srv += "\n"
	}
	v.Put(t, "wg_confs/wg0.conf", srv)

	// r15 saves a 3.1 switch only when the env sets it. The 3.1 preset
	// leaves it empty.
	if v.WriteParams {
		var saved strings.Builder
		for _, p := range params {
			val := v.Params[p.key]
			if slices.Contains(switchKeys, p.key) {
				val = ""
			}
			saved.WriteString(p.env + "=" + val + "\n")
		}
		v.Put(t, "server/awg_params", saved.String())
	}

	// The saved state: the inputs of the last start.
	var state strings.Builder
	orig := func(name, val string) { state.WriteString("ORIG_" + name + "=\"" + val + "\"\n") }
	orig("SERVERURL", ServerURL)
	orig("SERVERPORT", ServerPort)
	orig("PEERDNS", peerDNS)
	orig("PEERS", v.Peers)
	orig("INTERFACE", Interface)
	orig("ALLOWEDIPS", allowed)
	orig("PERSISTENTKEEPALIVE_PEERS", strings.Join(v.Keepalive, ","))
	for _, p := range params {
		orig(p.env, v.Params[p.key])
	}
	orig("SERVER_ALLOWEDIPS_ALL", "")
	orig("TEMPLATES_HASH", hash(serverTmpl+PeerTemplate))
	v.Put(t, ".donoteditthisfile", state.String())

	var servers []string
	for _, s := range strings.Split(v.DNSUpstream, ",") {
		servers = append(servers, "server="+s)
	}
	v.Put(t, "dnsmasq/dnsmasq.conf", expand(t, dnsTmpl, map[string]string{"${DNS_SERVER_LINES}": strings.Join(servers, "\n")}))
	v.Put(t, "dnsmasq/.donoteditthisfile", "ORIG_DNS_UPSTREAM=\""+v.DNSUpstream+"\"\nORIG_DNS_TEMPLATE_HASH=\""+hash(dnsTmpl)+"\"\n")
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
