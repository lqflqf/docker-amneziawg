package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/netcheck"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

// MaxPeers is the number of peer addresses in a /24 (.2-.254).
const MaxPeers = 253

// State is the configuration: everything in the DB.
type State struct {
	Settings map[string]string // non-AWG settings
	AWG      awg.Params
	Peers    []store.Peer
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

// Load reads the configuration.
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
	c := &State{Settings: map[string]string{}, AWG: s.AWG.Clone()}
	for k, v := range s.Settings {
		c.Settings[k] = v
	}
	c.Peers = append([]store.Peer(nil), s.Peers...)
	return c
}

// MinPeerID and MaxPeerID are the peer ids, which are also the last octet of
// the peer addresses (.1 is the server).
const (
	MinPeerID = 2
	MaxPeerID = 254
)

// NameRule describes a valid peer name for error messages.
const NameRule = "1-32 of A-Z a-z 0-9 . _ - and space, starting with a letter or digit, not ending with a space"

var (
	nameRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._ -]{0,30}[A-Za-z0-9._-])?$`)
	reservedRe = regexp.MustCompile(`^peer [0-9]+$`)
	idRe       = regexp.MustCompile(`^[0-9]{1,9}$`)
)

// ValidPeerName reports whether n can be a peer name.
func ValidPeerName(n string) bool { return nameRe.MatchString(n) }

// DefaultPeerName is the name of a peer without a name of its own.
func DefaultPeerName(id int) string { return "peer " + strconv.Itoa(id) }

// NameFitsID reports whether name may belong to the peer id: a name of the
// form "peer <digits>" is only the default name of that same id, so that a
// later default name never collides.
func NameFitsID(name string, id int) bool {
	return !reservedRe.MatchString(name) || name == DefaultPeerName(id)
}

// ParseID parses a peer id argument: plain decimal digits only.
func ParseID(s string) (int, bool) {
	if !idRe.MatchString(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

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
	names, ids, pubs := map[string]bool{}, map[int]bool{}, map[string]bool{}
	serverPub, _ := keys.Public(s.Get("server.private_key"))
	for _, p := range s.Peers {
		id := fmt.Sprintf("peer %d", p.ID)
		if p.ID < MinPeerID || p.ID > MaxPeerID {
			e("%s: the id must be %d-%d", id, MinPeerID, MaxPeerID)
		}
		if ids[p.ID] {
			e("%s: the id is used twice", id)
		}
		ids[p.ID] = true
		switch {
		case !ValidPeerName(p.Name):
			e("%s: name %q is not valid: use %s", id, p.Name, NameRule)
		case !NameFitsID(p.Name, p.ID):
			e("%s: name %q is the default name of peer %s only", id, p.Name, p.Name[len("peer "):])
		}
		if names[p.Name] {
			e("%s: name %q is used twice", id, p.Name)
		}
		names[p.Name] = true
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

// FreeIDs returns the n lowest ids that no peer holds.
func (s *State) FreeIDs(n int) ([]int, error) {
	used := map[int]bool{}
	for _, p := range s.Peers {
		used[p.ID] = true
	}
	var out []int
	for id := MinPeerID; id <= MaxPeerID && len(out) < n; id++ {
		if !used[id] {
			out = append(out, id)
		}
	}
	if len(out) < n {
		return nil, fmt.Errorf("only %d free address(es) in %s (at most %d peers)", len(out), s.Subnet(), MaxPeers)
	}
	return out, nil
}

// PeerAddress returns the address of peer id in subnet.
func PeerAddress(subnet string, id int) string {
	return netcheck.Prefix24(subnet) + "." + strconv.Itoa(id)
}

// ServerAddress returns the server end of subnet (".1").
func ServerAddress(subnet string) string { return netcheck.Prefix24(subnet) + ".1" }

// FindPeer returns the peer with id.
func (s *State) FindPeer(id int) (*store.Peer, error) {
	for i := range s.Peers {
		if s.Peers[i].ID == id {
			return &s.Peers[i], nil
		}
	}
	return nil, fmt.Errorf("no peer with id %d", id)
}
