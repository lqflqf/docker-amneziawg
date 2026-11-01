package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/netcheck"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

// MaxPeers is the number of peer addresses in a /24 (.2-.254).
const MaxPeers = 253

// State is the desired state: everything in the DB.
type State struct {
	Settings   map[string]string // non-AWG settings
	AWG        awg.Params
	Peers      []store.Peer
	Generation int64
}

// Get returns a setting, or its default when the row is missing.
func (s *State) Get(k string) string {
	if v, ok := s.Settings[k]; ok {
		return v
	}
	if r, ok := Lookup(k); ok {
		return r.Default
	}
	return ""
}

// Subnet returns the tunnel subnet ("10.13.13.0/24").
func (s *State) Subnet() string { return s.Get("net.subnet") }

// Load reads the desired state.
func Load(tx *store.Tx) (*State, error) {
	all, err := tx.Settings()
	if err != nil {
		return nil, err
	}
	st := &State{Settings: map[string]string{}, AWG: awg.Params{}}
	for k, v := range all {
		if a, ok := strings.CutPrefix(k, "awg."); ok {
			st.AWG[a] = v
		} else {
			st.Settings[k] = v
		}
	}
	if st.Peers, err = tx.Peers(); err != nil {
		return nil, err
	}
	if st.Generation, err = tx.Generation(); err != nil {
		return nil, err
	}
	return st, nil
}

// SaveSettings writes the settings and the AWG set (not the peers).
func (s *State) SaveSettings(tx *store.Tx) error {
	for _, r := range Registry {
		if err := tx.SetSetting(r.Key, s.Settings[r.Key]); err != nil {
			return err
		}
	}
	vals := map[string]string{}
	for k, v := range s.AWG {
		if v != "" {
			vals[k] = v
		}
	}
	return tx.ReplacePrefix("awg.", vals)
}

// Clone returns a deep copy.
func (s *State) Clone() *State {
	c := &State{Settings: map[string]string{}, AWG: s.AWG.Clone(), Generation: s.Generation}
	for k, v := range s.Settings {
		c.Settings[k] = v
	}
	c.Peers = append([]store.Peer(nil), s.Peers...)
	return c
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// ValidPeerName reports whether n can be a peer name.
func ValidPeerName(n string) bool { return nameRe.MatchString(n) }

// Validate checks the whole state: every setting, the AWG set and every peer,
// including that each stored public key belongs to its private key.
func (s *State) Validate() (errs []error, warns []string) {
	e := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	for k := range s.Settings {
		if _, ok := Lookup(k); !ok {
			e("unknown setting %q", k)
		}
	}
	for _, r := range Registry {
		v, ok := s.Settings[r.Key]
		if !ok || v == "" {
			if r.Default == "" {
				e("%s is not set", r.Key)
			}
			continue
		}
		n, err := r.Check(v)
		if err != nil {
			e("%s: %v", r.Key, err)
		} else if n != v {
			e("%s: %q is not in its normal form %q", r.Key, v, n)
		}
	}
	ae, aw := awg.Validate(s.AWG)
	for _, err := range ae {
		e("AWG %v", err)
	}
	warns = append(warns, aw...)

	if len(s.Peers) > MaxPeers {
		e("%d peers; the maximum for a /24 is %d", len(s.Peers), MaxPeers)
	}
	names, octets, pubs := map[string]bool{}, map[int]bool{}, map[string]bool{}
	serverPub, _ := keys.Public(s.Get("server.private_key"))
	for _, p := range s.Peers {
		id := fmt.Sprintf("peer #%d", p.ID)
		if !ValidPeerName(p.Name) {
			e("%s: name %q must match %s", id, p.Name, nameRe)
		}
		if names[p.Name] {
			e("%s: name %q is used twice", id, p.Name)
		}
		names[p.Name] = true
		if p.Octet < 2 || p.Octet > 254 || octets[p.Octet] {
			e("%s: address octet %d is out of range or used twice", id, p.Octet)
		}
		octets[p.Octet] = true
		pub, err := keys.Public(p.PrivateKey)
		switch {
		case err != nil:
			e("%s: private key: %v", id, err)
		case pub != p.PublicKey:
			e("%s: the public key does not belong to the private key", id)
		}
		if pubs[p.PublicKey] || p.PublicKey == serverPub {
			e("%s: the public key is used twice", id)
		}
		pubs[p.PublicKey] = true
		if p.PresharedKey != "" && !keys.Valid(p.PresharedKey) {
			e("%s: the preshared key is not a base64 32-byte key", id)
		}
	}
	return errs, warns
}

// Err joins the errors of Validate, or returns nil.
func (s *State) Err() error {
	errs, _ := s.Validate()
	return errors.Join(errs...)
}

// FreeOctet returns the lowest address octet that no peer holds.
func (s *State) FreeOctet() (int, error) {
	used := map[int]bool{}
	for _, p := range s.Peers {
		used[p.Octet] = true
	}
	for o := 2; o <= 254; o++ {
		if !used[o] {
			return o, nil
		}
	}
	return 0, fmt.Errorf("no free address in %s (at most %d peers)", s.Subnet(), MaxPeers)
}

// PeerAddress returns the address of a peer in subnet.
func PeerAddress(subnet string, octet int) string {
	return netcheck.Prefix24(subnet) + "." + strconv.Itoa(octet)
}

// ServerAddress returns the server end of subnet (".1").
func ServerAddress(subnet string) string { return netcheck.Prefix24(subnet) + ".1" }

// FindPeer resolves a selector: an id ("7", "#7") or a name ("phone",
// "name:123" for a name made of digits).
func (s *State) FindPeer(sel string) (*store.Peer, error) {
	byName := func(n string) (*store.Peer, error) {
		for i := range s.Peers {
			if s.Peers[i].Name == n {
				return &s.Peers[i], nil
			}
		}
		return nil, fmt.Errorf("no peer named %q", n)
	}
	if n, ok := strings.CutPrefix(sel, "name:"); ok {
		return byName(n)
	}
	idStr := strings.TrimPrefix(sel, "#")
	if id, err := strconv.ParseInt(idStr, 10, 64); err == nil && idStr != "" && idStr[0] != '+' && idStr[0] != '-' {
		for i := range s.Peers {
			if s.Peers[i].ID == id {
				return &s.Peers[i], nil
			}
		}
		return nil, fmt.Errorf("no peer with id %d", id)
	}
	return byName(sel)
}

// Active is the interface-level part of the state that wg0 started with:
// every restart-class setting and the whole AWG set ("awg.<key>").
type Active map[string]string

// ActiveOf extracts the active part of a state.
func ActiveOf(s *State) Active {
	a := Active{}
	for _, k := range RestartKeys() {
		a[k] = s.Get(k)
	}
	for k, v := range s.AWG {
		if v != "" {
			a["awg."+k] = v
		}
	}
	return a
}

// AWG returns the AWG part of an active snapshot.
func (a Active) AWG() awg.Params {
	p := awg.Params{}
	for k, v := range a {
		if x, ok := strings.CutPrefix(k, "awg."); ok {
			p[x] = v
		}
	}
	return p
}

// PendingRestart lists the keys whose desired value differs from the active
// one. It is computed every time and never stored, so it cannot go stale.
func PendingRestart(desired *State, active Active) []string {
	want := ActiveOf(desired)
	seen := map[string]bool{}
	var out []string
	for k, v := range want {
		seen[k] = true
		if active[k] != v {
			out = append(out, k)
		}
	}
	for k := range active {
		if !seen[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
