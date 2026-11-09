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
	Dir    string
	Rename map[string]string // old peer dir (peer_x) or proposed name -> new name
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
	Dropped  []string // items that stop the import, unless the caller accepts a partial import
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

var (
	digitsRe = regexp.MustCompile(`^[0-9]+$`)
	alnumRe  = regexp.MustCompile(`^[[:alnum:]]+$`)
)

// oldPeerIDs returns the peer directory ids of the saved PEERS value, the
// way the shell release made them: "3" -> peer1..peer3, "1,laptop" -> peer1,
// peer_laptop.
func oldPeerIDs(peers string) []string {
	var ids []string
	if digitsRe.MatchString(peers) {
		n, _ := strconv.Atoi(peers)
		for i := 1; i <= n; i++ {
			ids = append(ids, "peer"+strconv.Itoa(i))
		}
		return ids
	}
	for _, e := range strings.Split(peers, ",") {
		e = strings.TrimSpace(e)
		switch {
		case e == "" || !alnumRe.MatchString(e):
		case digitsRe.MatchString(e):
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

var (
	masqRe = regexp.MustCompile(`^iptables -t nat -[AD] POSTROUTING -o (\S+) -j MASQUERADE$`)
	// The other rules of the past default templates.
	defaultRules = []*regexp.Regexp{
		regexp.MustCompile(`^iptables -[AD] FORWARD -[io] %i -j ACCEPT$`),
		regexp.MustCompile(`^ip6tables -[AID] FORWARD -[io] %i -j DROP$`),
	}
	// One past default forwarded and masqueraded IPv6 too. The tunnel is
	// IPv4-only, so these rules map to the IPv6 DROP rules.
	ipv6ForwardRules = []*regexp.Regexp{
		regexp.MustCompile(`^ip6tables -[AD] FORWARD -[io] %i -j ACCEPT$`),
		regexp.MustCompile(`^ip6tables -t nat -[AD] POSTROUTING -o \S+ -j MASQUERADE$`),
	}
)

// knownRule reports whether c is a firewall command of a past default
// template. It returns the NAT interface for the MASQUERADE rule and whether
// c is an IPv6 DROP rule.
func knownRule(c string) (known bool, nat string, v6drop bool) {
	if m := masqRe.FindStringSubmatch(c); m != nil {
		return true, m[1], false
	}
	for _, re := range defaultRules {
		if re.MatchString(c) {
			return true, "", strings.HasPrefix(c, "ip6tables")
		}
	}
	for _, re := range ipv6ForwardRules {
		if re.MatchString(c) {
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

// analyzer holds the state of one Analyze run. Each step adds to st and rep.
type analyzer struct {
	dir  string
	rep  *Report
	st   *config.State
	orig map[string]string // the saved env of the shell release
	wg0  *conf.File
	srv  *conf.Section // [Interface] of wg0.conf

	subnet, prefix string                   // "10.13.13.0/24", "10.13.13."
	srvPeers       map[string]*conf.Section // wg0.conf [Peer] by public key
	peerConfs      map[string]*conf.File    // installed peer confs by new name
	firstPeerConf  *conf.File
}

func (a *analyzer) block(format string, v ...any) {
	a.rep.Blockers = append(a.rep.Blockers, fmt.Sprintf(format, v...))
}

func (a *analyzer) note(format string, v ...any) {
	a.rep.Notes = append(a.rep.Notes, fmt.Sprintf(format, v...))
}

func (a *analyzer) drop(format string, v ...any) {
	a.rep.Dropped = append(a.rep.Dropped, fmt.Sprintf(format, v...))
}

// set checks v with the registry and stores its normal form.
func (a *analyzer) set(k, v string) {
	s, _ := config.Lookup(k)
	n, err := s.Check(v)
	if err != nil {
		a.block("%s: %q: %v", k, v, err)
		return
	}
	a.st.Settings[k] = n
}

// def returns the registry default of k. A wrong key stops the import.
func (a *analyzer) def(k string) string {
	v, err := config.Default(k)
	if err != nil {
		a.block("%v", err)
	}
	return v
}

// Analyze reads an old volume and builds the state it describes. It changes
// nothing on disk.
func Analyze(opts Options) (*Report, error) {
	a := &analyzer{dir: opts.Dir, rep: &Report{},
		st: &config.State{Settings: map[string]string{}, AWG: awg.Params{}}}
	ids, err := a.load()
	if err != nil {
		return nil, err
	}
	if err := a.server(); err != nil {
		return nil, err
	}
	a.firewall()
	a.awgValues()
	a.peers(ids, opts.Rename)
	a.peerConfValues()
	a.dns()
	a.check()
	a.verify()
	return a.rep, nil
}

// load reads the saved env and wg0.conf, and returns the old peer dir ids.
func (a *analyzer) load() ([]string, error) {
	orig, err := readKV(filepath.Join(a.dir, ".donoteditthisfile"))
	if err != nil {
		orig = map[string]string{}
		a.note("no .donoteditthisfile: the values come from the confs only")
	}
	a.orig = orig
	if a.wg0, err = readConf(filepath.Join(a.dir, "wg_confs", "wg0.conf")); err != nil {
		return nil, fmt.Errorf("no readable wg_confs/wg0.conf in %s: %v", a.dir, err)
	}
	ids := oldPeerIDs(orig["PEERS"])
	if orig["PEERS"] == "" {
		matches, _ := filepath.Glob(filepath.Join(a.dir, "peer*"))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.IsDir() {
				ids = append(ids, filepath.Base(m))
			}
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("this is a client-mode volume (no PEERS and no peer directories); this image is a server only")
		}
	}
	if a.srv = a.wg0.Interface(); a.srv == nil {
		return nil, fmt.Errorf("wg0.conf has no [Interface]")
	}
	return ids, nil
}

// server reads the server key, the subnet and the MTU.
func (a *analyzer) server() error {
	priv := a.srv.Get("PrivateKey")
	if f := readKey(filepath.Join(a.dir, "server", "privatekey-server")); f != "" && f != priv {
		a.block("server/privatekey-server does not match the PrivateKey in wg0.conf")
	}
	a.set(config.KeyServerPrivateKey, priv)

	// Subnet from the installed address, not from the env or INTERFACE.
	addr := strings.TrimSuffix(strings.Split(a.srv.Get("Address"), ",")[0], "/24")
	addr = strings.TrimSpace(addr)
	if !netcheck.IsIPv4(addr) || !strings.HasSuffix(addr, ".1") {
		return fmt.Errorf("wg0.conf Address %q is not <subnet>.1", a.srv.Get("Address"))
	}
	a.subnet = strings.TrimSuffix(addr, ".1") + ".0/24"
	a.set(config.KeySubnet, a.subnet)
	a.prefix = netcheck.Prefix24(a.subnet) + "."

	if m := a.srv.Get("MTU"); m != "" {
		a.set(config.KeyMTU, m)
	} else {
		a.st.Settings[config.KeyMTU] = "none"
	}
	return nil
}

// firewall maps the PostUp/PostDown commands to the firewall settings.
func (a *analyzer) firewall() {
	nat, v6 := "", false
	for _, k := range []string{"PostUp", "PostDown"} {
		for _, v := range a.srv.All(k) {
			for _, c := range splitCommands(v) {
				ok, n, d := knownRule(c)
				if !ok {
					a.drop("custom %s command %q: custom firewall rules are not supported", k, c)
					continue
				}
				if n != "" {
					if nat != "" && nat != n {
						a.block("two different MASQUERADE interfaces (%s, %s)", nat, n)
					}
					nat = n
				}
				v6 = v6 || d
			}
		}
	}
	if nat == "" {
		nat = a.def(config.KeyNATInterface)
		a.note("no MASQUERADE rule found; using %s", nat)
	}
	a.set(config.KeyNATInterface, nat)
	a.st.Settings[config.KeyIPv6ForwardDrop] = strconv.FormatBool(v6)
}

// awgValues reads the AWG set from the installed conf, and the version from
// the saved params. Without a saved version, it infers the version.
func (a *analyzer) awgValues() {
	p := a.st.AWG
	for _, l := range a.srv.Lines {
		if k := awg.KeyForConf(l.Key); k != "" {
			p[k] = l.Value
		}
	}
	for _, k := range awg.Switches {
		if sw, ok := awg.NormalizeSwitch(p[k]); ok {
			p[k] = sw
		} else {
			p[k] = "off"
		}
	}
	version := ""
	if saved, err := readKV(filepath.Join(a.dir, "server", "awg_params")); err == nil {
		version = saved["AWG_VERSION"]
	}
	if version == "" {
		version = awg.InferVersion(p)
		a.note("no saved AWG_VERSION; inferred %s from wg0.conf", version)
	}
	p["version"] = version
}

// peers imports each old peer dir that matches a [Peer] of wg0.conf.
func (a *analyzer) peers(ids []string, rename map[string]string) {
	a.srvPeers = map[string]*conf.Section{}
	keepaliveSeconds := ""
	for _, p := range a.wg0.Peers() {
		a.srvPeers[p.Get("PublicKey")] = p
		if ka := p.Get("PersistentKeepalive"); ka != "" {
			keepaliveSeconds = ka
		}
	}
	if keepaliveSeconds != "" {
		a.set(config.KeyKeepaliveSeconds, keepaliveSeconds)
	}

	names := map[string]string{} // new name -> old dir id
	a.peerConfs = map[string]*conf.File{}
	seenPub := map[string]bool{}
	for _, id := range ids {
		p, ok := a.peer(id, rename)
		if !ok {
			continue
		}
		seenPub[p.PublicKey] = true
		if other, ok := names[p.Name]; ok {
			a.block("%s and %s both map to the name %q; use --rename %s=<new name>", other, id, p.Name, id)
		}
		names[p.Name] = id
		a.st.Peers = append(a.st.Peers, p)
		if pc, err := readConf(filepath.Join(a.dir, id, id+".conf")); err == nil {
			a.peerConfs[p.Name] = pc
			if a.firstPeerConf == nil {
				a.firstPeerConf = pc
			}
		}
	}
	for pub := range a.srvPeers {
		if !seenPub[pub] {
			a.drop("wg0.conf has a [Peer] (%s…) with no peer directory; it cannot be exported", pub[:min(8, len(pub))])
		}
	}
}

// peer reads one old peer dir. It returns false when the peer is not imported.
func (a *analyzer) peer(id string, rename map[string]string) (store.Peer, bool) {
	pdir := filepath.Join(a.dir, id)
	if _, err := os.Stat(pdir); err != nil {
		a.drop("%s is listed in PEERS but has no directory", id)
		return store.Peer{}, false
	}
	priv := readKey(filepath.Join(pdir, "privatekey-"+id))
	if priv == "" {
		a.drop("%s has no privatekey-%s", id, id)
		return store.Peer{}, false
	}
	pub, err := keys.Public(priv)
	if err != nil {
		a.block("%s: private key: %v", id, err)
		return store.Peer{}, false
	}
	srv, ok := a.srvPeers[pub]
	if !ok {
		a.drop("%s is not in wg0.conf (its key does not match any [Peer])", id)
		return store.Peer{}, false
	}
	psk := readKey(filepath.Join(pdir, "presharedkey-"+id))
	if psk != "" && srv.Get("PresharedKey") != psk {
		a.block("%s: the preshared key does not match wg0.conf", id)
	}
	if psk == "" {
		a.note("%s has no preshared key; it is imported without one", id)
	}
	octet := 0
	var extra []string
	for _, ip := range strings.Split(srv.Get("AllowedIPs"), ",") {
		ip = strings.TrimSpace(ip)
		if host, ok := strings.CutSuffix(ip, "/32"); ok && strings.HasPrefix(host, a.prefix) && octet == 0 {
			octet, _ = strconv.Atoi(strings.TrimPrefix(host, a.prefix))
			continue
		}
		if ip != "" {
			extra = append(extra, ip)
		}
	}
	if octet == 0 {
		a.block("%s: no /32 address in %s in its wg0.conf AllowedIPs", id, a.subnet)
		return store.Peer{}, false
	}
	if len(extra) > 0 {
		a.drop("%s routes %s (site-to-site): not supported; only its /32 is imported", id, strings.Join(extra, ", "))
	}
	name := nameFor(id)
	if r, ok := rename[id]; ok {
		name = r
	} else if r, ok := rename[name]; ok {
		name = r
	}
	if !config.ValidPeerName(name) {
		a.block("%s: name %q is not valid; use --rename %s=<name>", id, name, id)
	}
	return store.Peer{ID: octet, Name: name, PrivateKey: priv, PublicKey: pub,
		PresharedKey: psk, Keepalive: srv.Get("PersistentKeepalive") != "", Enabled: true}, true
}

// peerConfValues reads the values that only the peer confs hold: the
// endpoint, AllowedIPs and the peer MTU.
func (a *analyzer) peerConfValues() {
	endpointHost, endpointPort := a.orig["SERVERURL"], a.orig["SERVERPORT"]
	allowed := a.orig["ALLOWEDIPS"]
	peerMTU := "none"
	if a.firstPeerConf != nil {
		if pi := a.firstPeerConf.Interface(); pi != nil && pi.Get("MTU") != "" {
			peerMTU = pi.Get("MTU")
		}
		if ps := a.firstPeerConf.Peers(); len(ps) > 0 {
			if ep := ps[0].Get("Endpoint"); strings.LastIndexByte(ep, ':') > 0 {
				i := strings.LastIndexByte(ep, ':')
				endpointHost, endpointPort = ep[:i], ep[i+1:]
			}
			if v := ps[0].Get("AllowedIPs"); v != "" {
				allowed = v
			}
		}
	} else if a.srv.Get("MTU") != "" {
		peerMTU = a.srv.Get("MTU")
	}
	if endpointPort == "" {
		endpointPort = a.def(config.KeyEndpointPort)
	}
	a.set(config.KeyEndpointHost, endpointHost)
	a.set(config.KeyEndpointPort, endpointPort)
	if allowed == "" {
		allowed = a.def(config.KeyAllowedIPs)
	}
	a.set(config.KeyAllowedIPs, allowed)
	a.set(config.KeyPeerMTU, peerMTU)
}

// dns reads the upstream servers and refuses custom dnsmasq options.
func (a *analyzer) dns() {
	upstream := ""
	if f, err := os.ReadFile(filepath.Join(a.dir, "dnsmasq", "dnsmasq.conf")); err == nil {
		var servers []string
		for _, l := range strings.Split(string(f), "\n") {
			if s, ok := strings.CutPrefix(strings.TrimSpace(l), "server="); ok {
				servers = append(servers, s)
			}
		}
		upstream = strings.Join(servers, ",")
	}
	if upstream == "" {
		if d, err := readKV(filepath.Join(a.dir, "dnsmasq", ".donoteditthisfile")); err == nil {
			upstream = d["DNS_UPSTREAM"]
		}
	}
	if upstream == "" {
		upstream = a.def(config.KeyDNSUpstream)
	}
	a.set(config.KeyDNSUpstream, upstream)
	if b, err := os.ReadFile(filepath.Join(a.dir, "templates", "dnsmasq.conf")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") || dnsmasqDefaults[l] {
				continue
			}
			a.drop("custom dnsmasq option %q: not supported", l)
		}
	}
}

// check validates the whole state and puts it in the report.
func (a *analyzer) check() {
	a.st.SortPeers()
	if errs, warns := a.st.Validate(); len(errs) > 0 {
		for _, e := range errs {
			a.block("%v", e)
		}
	} else {
		for _, w := range warns {
			a.note("warning: %s", w)
		}
	}
	a.rep.State = a.st
}

// verify compares each new peer export with the installed peer conf, and the
// new wg0.conf with the old one. It runs only when there are no blockers.
func (a *analyzer) verify() {
	if len(a.rep.Blockers) > 0 {
		return
	}
	for _, p := range a.st.Peers {
		old, ok := a.peerConfs[p.Name]
		if !ok {
			a.rep.Verdicts = append(a.rep.Verdicts, Verdict{Name: p.Name, Result: "unverifiable"})
			continue
		}
		nf, err := render.Peer(a.st, p)
		if err != nil {
			a.block("%s: %v", p.Name, err)
			continue
		}
		if d := conf.Diff(old, nf, normalizeForDiff); len(d) > 0 {
			a.rep.Verdicts = append(a.rep.Verdicts, Verdict{Name: p.Name, Result: "changed", Changed: d})
		} else {
			a.rep.Verdicts = append(a.rep.Verdicts, Verdict{Name: p.Name, Result: "same"})
		}
	}
	if srv, err := render.Server(a.st); err == nil {
		for _, k := range conf.Diff(a.wg0, srv, normalizeForDiff) {
			// The rules are split into one command per line now, and the
			// server address gained its /24; neither changes the tunnel.
			if k != "Interface.postup" && k != "Interface.postdown" && k != "Interface.address" {
				a.rep.Server = append(a.rep.Server, k)
			}
		}
	}
}
