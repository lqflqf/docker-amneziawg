// Package legacy imports a /config volume written by the shell-based releases
// into a new DB, once. Nothing is sourced or evaluated: the old files are
// parsed as data, and the installed confs are the truth for keys, addresses
// and AWG values.
package legacy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/conf"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/netcheck"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

// Options of an import.
type Options struct {
	Dir          string
	ForcePartial bool              // import without the items that cannot be imported
	Rename       map[string]string // old peer dir (peer_x) or proposed name -> new name
}

// Verdict of one peer conf.
type Verdict struct {
	Name    string
	Result  string // "same", "changed", "unverifiable"
	Changed []string
}

// Report is the result of Analyze.
type Report struct {
	State    *config.State
	Notes    []string
	Blockers []string // items that always stop the import
	Dropped  []string // items that stop the import unless --force-partial drops them
	Verdicts []Verdict
	Server   []string // differing keys of wg0.conf (informational)
}

// readKV parses NAME=value lines; ORIG_ prefixes and surrounding quotes are
// removed. Values are data: they are never evaluated.
func readKV(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		k = strings.TrimPrefix(strings.TrimSpace(k), "ORIG_")
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, sc.Err()
}

func readConf(path string) (*conf.File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return conf.Parse(string(b))
}

func readKey(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// oldPeerIDs returns the peer directory ids of the saved PEERS value, the
// way the shell release made them: "3" -> peer1..peer3, "1,laptop" -> peer1,
// peer_laptop.
func oldPeerIDs(peers string) []string {
	var ids []string
	if regexp.MustCompile(`^[0-9]+$`).MatchString(peers) {
		n, _ := strconv.Atoi(peers)
		for i := 1; i <= n; i++ {
			ids = append(ids, "peer"+strconv.Itoa(i))
		}
		return ids
	}
	for _, e := range strings.Split(peers, ",") {
		e = strings.TrimSpace(e)
		switch {
		case e == "" || !regexp.MustCompile(`^[[:alnum:]]+$`).MatchString(e):
		case regexp.MustCompile(`^[0-9]+$`).MatchString(e):
			ids = append(ids, "peer"+e)
		default:
			ids = append(ids, "peer_"+e)
		}
	}
	return ids
}

// nameFor maps an old dir id to the new name: peer1 -> peer1, peer_laptop -> laptop.
func nameFor(id string) string {
	if n, ok := strings.CutPrefix(id, "peer_"); ok {
		return n
	}
	return id
}

// splitCommands splits a PostUp/PostDown value into single commands.
func splitCommands(v string) []string {
	var out []string
	for _, c := range strings.Split(v, ";") {
		c = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(c), "|| true"))
		if c != "" {
			out = append(out, strings.Join(strings.Fields(c), " "))
		}
	}
	return out
}

var masqRe = regexp.MustCompile(`^iptables -t nat -[AD] POSTROUTING -o (\S+) -j MASQUERADE$`)

// knownRule reports whether c is a firewall command of a past default
// template. It returns the NAT interface for the MASQUERADE rule and whether
// c is an IPv6 DROP rule.
func knownRule(c string) (known bool, nat string, v6drop bool) {
	if m := masqRe.FindStringSubmatch(c); m != nil {
		return true, m[1], false
	}
	for _, re := range []string{
		`^iptables -[AD] FORWARD -[io] %i -j ACCEPT$`,
		`^ip6tables -[AID] FORWARD -[io] %i -j DROP$`,
	} {
		if regexp.MustCompile(re).MatchString(c) {
			return true, "", strings.HasPrefix(c, "ip6tables")
		}
	}
	// One past default forwarded and masqueraded IPv6 too; the tunnel is
	// IPv4-only, so it maps to the IPv6 DROP rules.
	for _, re := range []string{
		`^ip6tables -[AD] FORWARD -[io] %i -j ACCEPT$`,
		`^ip6tables -t nat -[AD] POSTROUTING -o \S+ -j MASQUERADE$`,
	} {
		if regexp.MustCompile(re).MatchString(c) {
			return true, "", true
		}
	}
	return false, "", false
}

var dnsmasqDefaults = map[string]bool{
	"domain-needed": true, "bogus-priv": true, "no-hosts": true, "cache-size=1000": true,
	"${DNS_SERVER_LINES}": true,
}

func normalizeForDiff(key, v string) string {
	switch key {
	case "address":
		return strings.TrimSuffix(strings.TrimSpace(v), "/32")
	case "allowedips":
		if n, err := netcheck.CIDRList(v); err == nil {
			return n
		}
	}
	return strings.TrimSpace(v)
}

// Analyze reads an old volume and builds the state it describes. It changes
// nothing on disk.
func Analyze(opts Options) (*Report, error) {
	dir := opts.Dir
	rep := &Report{}
	block := func(format string, a ...any) { rep.Blockers = append(rep.Blockers, fmt.Sprintf(format, a...)) }
	note := func(format string, a ...any) { rep.Notes = append(rep.Notes, fmt.Sprintf(format, a...)) }
	drop := func(format string, a ...any) { rep.Dropped = append(rep.Dropped, fmt.Sprintf(format, a...)) }

	orig, err := readKV(filepath.Join(dir, ".donoteditthisfile"))
	if err != nil {
		orig = map[string]string{}
		note("no .donoteditthisfile: the values come from the confs only")
	}
	wg0, err := readConf(filepath.Join(dir, "wg_confs", "wg0.conf"))
	if err != nil {
		return nil, fmt.Errorf("no readable wg_confs/wg0.conf in %s: %v", dir, err)
	}
	ids := oldPeerIDs(orig["PEERS"])
	if orig["PEERS"] == "" {
		matches, _ := filepath.Glob(filepath.Join(dir, "peer*"))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.IsDir() {
				ids = append(ids, filepath.Base(m))
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("this is a client-mode volume (no PEERS and no peer directories); this image is a server only")
		}
	}
	iface := wg0.Interface()
	if iface == nil {
		return nil, fmt.Errorf("wg0.conf has no [Interface]")
	}

	st := &config.State{Settings: map[string]string{}, AWG: awg.Params{}}
	set := func(k, v string) {
		s, _ := config.Lookup(k)
		n, err := s.Check(v)
		if err != nil {
			block("%s: %q: %v", k, v, err)
			return
		}
		st.Settings[k] = n
	}
	// def returns the registry default of k. A wrong key stops the import.
	def := func(k string) string {
		v, err := config.Default(k)
		if err != nil {
			block("%v", err)
		}
		return v
	}

	// Server key
	priv := iface.Get("PrivateKey")
	if f := readKey(filepath.Join(dir, "server", "privatekey-server")); f != "" && f != priv {
		block("server/privatekey-server does not match the PrivateKey in wg0.conf")
	}
	set(config.KeyServerPrivateKey, priv)

	// Subnet from the installed address, not from the env or INTERFACE.
	addr := strings.TrimSuffix(strings.Split(iface.Get("Address"), ",")[0], "/24")
	addr = strings.TrimSpace(addr)
	if !netcheck.IsIPv4(addr) || !strings.HasSuffix(addr, ".1") {
		return nil, fmt.Errorf("wg0.conf Address %q is not <subnet>.1", iface.Get("Address"))
	}
	subnet := strings.TrimSuffix(addr, ".1") + ".0/24"
	set(config.KeySubnet, subnet)
	prefix := netcheck.Prefix24(subnet) + "."

	if m := iface.Get("MTU"); m != "" {
		set(config.KeyMTU, m)
	} else {
		st.Settings[config.KeyMTU] = "none"
	}

	// Firewall
	nat, v6 := "", false
	for _, k := range []string{"PostUp", "PostDown"} {
		for _, v := range iface.All(k) {
			for _, c := range splitCommands(v) {
				ok, n, d := knownRule(c)
				if !ok {
					drop("custom %s command %q: custom firewall rules are not supported", k, c)
					continue
				}
				if n != "" {
					if nat != "" && nat != n {
						block("two different MASQUERADE interfaces (%s, %s)", nat, n)
					}
					nat = n
				}
				v6 = v6 || d
			}
		}
	}
	if nat == "" {
		nat = def(config.KeyNATInterface)
		note("no MASQUERADE rule found; using %s", nat)
	}
	set(config.KeyNATInterface, nat)
	st.Settings[config.KeyIPv6ForwardDrop] = strconv.FormatBool(v6)

	// AWG values from the installed conf
	for _, l := range iface.Lines {
		if k := awg.KeyForConf(l.Key); k != "" {
			st.AWG[k] = l.Value
		}
	}
	for _, k := range awg.Switches {
		if sw, ok := awg.NormalizeSwitch(st.AWG[k]); ok {
			st.AWG[k] = sw
		} else {
			st.AWG[k] = "off"
		}
	}
	version := ""
	if p, err := readKV(filepath.Join(dir, "server", "awg_params")); err == nil {
		version = p["AWG_VERSION"]
	}
	if version == "" {
		version = awg.InferVersion(st.AWG)
		note("no saved AWG_VERSION; inferred %s from wg0.conf", version)
	}
	st.AWG["version"] = version

	// Server-side peer entries by public key
	srvPeers := map[string]*conf.Section{}
	keepaliveSeconds := ""
	for _, p := range wg0.Peers() {
		srvPeers[p.Get("PublicKey")] = p
		if ka := p.Get("PersistentKeepalive"); ka != "" {
			keepaliveSeconds = ka
		}
	}
	if keepaliveSeconds != "" {
		set(config.KeyKeepaliveSeconds, keepaliveSeconds)
	}

	// Peers
	rename := opts.Rename
	names := map[string]string{}
	var firstPeerConf *conf.File
	legacyConfs := map[string]*conf.File{}
	seenPub := map[string]bool{}
	for _, id := range ids {
		pdir := filepath.Join(dir, id)
		if _, err := os.Stat(pdir); err != nil {
			drop("%s is listed in PEERS but has no directory", id)
			continue
		}
		ppriv := readKey(filepath.Join(pdir, "privatekey-"+id))
		if ppriv == "" {
			drop("%s has no privatekey-%s", id, id)
			continue
		}
		ppub, err := keys.Public(ppriv)
		if err != nil {
			block("%s: private key: %v", id, err)
			continue
		}
		srv, ok := srvPeers[ppub]
		if !ok {
			drop("%s is not in wg0.conf (its key does not match any [Peer])", id)
			continue
		}
		seenPub[ppub] = true
		psk := readKey(filepath.Join(pdir, "presharedkey-"+id))
		if psk != "" && srv.Get("PresharedKey") != psk {
			block("%s: the preshared key does not match wg0.conf", id)
		}
		if psk == "" {
			note("%s has no preshared key; it is imported without one", id)
		}
		octet := 0
		var extra []string
		for _, a := range strings.Split(srv.Get("AllowedIPs"), ",") {
			a = strings.TrimSpace(a)
			if ip, ok := strings.CutSuffix(a, "/32"); ok && strings.HasPrefix(ip, prefix) && octet == 0 {
				octet, _ = strconv.Atoi(strings.TrimPrefix(ip, prefix))
				continue
			}
			if a != "" {
				extra = append(extra, a)
			}
		}
		if octet == 0 {
			block("%s: no /32 address in %s in its wg0.conf AllowedIPs", id, subnet)
			continue
		}
		if len(extra) > 0 {
			drop("%s routes %s (site-to-site): not supported; only its /32 is imported", id, strings.Join(extra, ", "))
		}
		name := nameFor(id)
		if r, ok := rename[id]; ok {
			name = r
		} else if r, ok := rename[name]; ok {
			name = r
		}
		if !config.ValidPeerName(name) {
			block("%s: name %q is not valid; use --rename %s=<name>", id, name, id)
		}
		if other, ok := names[name]; ok {
			block("%s and %s both map to the name %q; use --rename %s=<new name>", other, id, name, id)
		}
		names[name] = id
		st.Peers = append(st.Peers, store.Peer{ID: octet, Name: name, PrivateKey: ppriv, PublicKey: ppub,
			PresharedKey: psk, Keepalive: srv.Get("PersistentKeepalive") != "", Enabled: true})
		if pc, err := readConf(filepath.Join(pdir, id+".conf")); err == nil {
			legacyConfs[name] = pc
			if firstPeerConf == nil {
				firstPeerConf = pc
			}
		}
	}
	for pub := range srvPeers {
		if !seenPub[pub] {
			drop("wg0.conf has a [Peer] (%s…) with no peer directory; it cannot be exported", pub[:min(8, len(pub))])
		}
	}

	// Values that only the peer confs hold
	endpointHost, endpointPort := orig["SERVERURL"], orig["SERVERPORT"]
	allowed := orig["ALLOWEDIPS"]
	peerMTU := "none"
	if firstPeerConf != nil {
		if pi := firstPeerConf.Interface(); pi != nil && pi.Get("MTU") != "" {
			peerMTU = pi.Get("MTU")
		}
		if ps := firstPeerConf.Peers(); len(ps) > 0 {
			if ep := ps[0].Get("Endpoint"); strings.LastIndexByte(ep, ':') > 0 {
				i := strings.LastIndexByte(ep, ':')
				endpointHost, endpointPort = ep[:i], ep[i+1:]
			}
			if a := ps[0].Get("AllowedIPs"); a != "" {
				allowed = a
			}
		}
	} else if iface.Get("MTU") != "" {
		peerMTU = iface.Get("MTU")
	}
	if endpointPort == "" {
		endpointPort = def(config.KeyEndpointPort)
	}
	set(config.KeyEndpointHost, endpointHost)
	set(config.KeyEndpointPort, endpointPort)
	if allowed == "" {
		allowed = def(config.KeyAllowedIPs)
	}
	set(config.KeyAllowedIPs, allowed)
	set(config.KeyPeerMTU, peerMTU)

	// DNS
	upstream := ""
	if f, err := os.ReadFile(filepath.Join(dir, "dnsmasq", "dnsmasq.conf")); err == nil {
		var servers []string
		for _, l := range strings.Split(string(f), "\n") {
			if s, ok := strings.CutPrefix(strings.TrimSpace(l), "server="); ok {
				servers = append(servers, s)
			}
		}
		upstream = strings.Join(servers, ",")
	}
	if upstream == "" {
		if d, err := readKV(filepath.Join(dir, "dnsmasq", ".donoteditthisfile")); err == nil {
			upstream = d["DNS_UPSTREAM"]
		}
	}
	if upstream == "" {
		upstream = def(config.KeyDNSUpstream)
	}
	set(config.KeyDNSUpstream, upstream)
	if b, err := os.ReadFile(filepath.Join(dir, "templates", "dnsmasq.conf")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") || dnsmasqDefaults[l] {
				continue
			}
			drop("custom dnsmasq option %q: not supported", l)
		}
	}

	st.SortPeers()
	if errs, warns := st.Validate(); len(errs) > 0 {
		for _, e := range errs {
			block("%v", e)
		}
	} else {
		for _, w := range warns {
			note("warning: %s", w)
		}
	}
	rep.State = st

	// Per-peer verification: the new export against the installed peer conf.
	if len(rep.Blockers) == 0 {
		for _, p := range st.Peers {
			old, ok := legacyConfs[p.Name]
			if !ok {
				rep.Verdicts = append(rep.Verdicts, Verdict{Name: p.Name, Result: "unverifiable"})
				continue
			}
			nf, err := render.Peer(st, p)
			if err != nil {
				block("%s: %v", p.Name, err)
				continue
			}
			if d := conf.Diff(old, nf, normalizeForDiff); len(d) > 0 {
				rep.Verdicts = append(rep.Verdicts, Verdict{Name: p.Name, Result: "changed", Changed: d})
			} else {
				rep.Verdicts = append(rep.Verdicts, Verdict{Name: p.Name, Result: "same"})
			}
		}
		if srv, err := render.Server(st); err == nil {
			for _, k := range conf.Diff(wg0, srv, normalizeForDiff) {
				// The rules are split into one command per line now, and the
				// server address gained its /24; neither changes the tunnel.
				if k != "Interface.postup" && k != "Interface.postdown" && k != "Interface.address" {
					rep.Server = append(rep.Server, k)
				}
			}
		}
	}
	return rep, nil
}
