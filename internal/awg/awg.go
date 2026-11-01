// Package awg holds the AmneziaWG obfuscation parameters: their names, the
// generators, the cross-key rules and the deterministic version transitions.
//
// Ranges and rules follow .github/skills/amneziawg-config/references/parameters.md
// and the shell generator that this package replaces.
package awg

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/keys"
)

// Params maps a short key ("s1", "header_protection_key") to its value. A
// missing key or an empty value means "not set" (no line in the conf).
type Params map[string]string

// Versions in order.
var Versions = []string{"1.5", "2.0", "3.0", "3.1"}

// Default is the protocol version of a new server.
const Default = "3.1"

// Keys lists every parameter after "version", in conf order.
var Keys = []string{
	"jc", "jmin", "jmax",
	"s1", "s2", "s3", "s4",
	"h1", "h2", "h3", "h4",
	"i1", "i2", "i3", "i4", "i5",
	"header_protection_key", "content_padding",
	"rekey_after_time", "rekey_timeout", "reject_after_time",
	"keepalive_timeout", "max_handshake_attempts",
	"random_trailers", "disable_cookies",
}

// AllKeys is "version" followed by Keys.
var AllKeys = append([]string{"version"}, Keys...)

var confKey = map[string]string{
	"jc": "Jc", "jmin": "Jmin", "jmax": "Jmax",
	"s1": "S1", "s2": "S2", "s3": "S3", "s4": "S4",
	"h1": "H1", "h2": "H2", "h3": "H3", "h4": "H4",
	"i1": "I1", "i2": "I2", "i3": "I3", "i4": "I4", "i5": "I5",
	"header_protection_key":  "HeaderProtectionKey",
	"content_padding":        "ContentPaddingAddition",
	"rekey_after_time":       "RekeyAfterTime",
	"rekey_timeout":          "RekeyTimeout",
	"reject_after_time":      "RejectAfterTime",
	"keepalive_timeout":      "KeepaliveTimeout",
	"max_handshake_attempts": "MaxHandshakeAttempts",
	"random_trailers":        "RandomTrailers",
	"disable_cookies":        "DisableCookies",
}

// ConfKey returns the conf key of a parameter ("s1" -> "S1").
func ConfKey(k string) string { return confKey[k] }

// KeyForConf returns the parameter of a conf key ("S1" -> "s1"), or "".
func KeyForConf(ck string) string {
	for k, v := range confKey {
		if strings.EqualFold(v, ck) {
			return k
		}
	}
	return ""
}

var (
	sKeys  = []string{"s1", "s2", "s3", "s4"}
	hKeys  = []string{"h1", "h2", "h3", "h4"}
	iKeys  = []string{"i1", "i2", "i3", "i4", "i5"}
	x3Keys = []string{"header_protection_key", "content_padding", "rekey_after_time",
		"rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts"}
	timerKeys = []string{"rekey_after_time", "rekey_timeout", "reject_after_time",
		"keepalive_timeout", "max_handshake_attempts"}
)

// mustMatch: the server and every peer need the same value.
var mustMatch = map[string]bool{
	"s1": true, "s2": true, "s3": true, "s4": true,
	"h1": true, "h2": true, "h3": true, "h4": true,
	"i1": true, "i2": true, "i3": true, "i4": true, "i5": true,
	"header_protection_key": true, "random_trailers": true, "version": true,
}

// MustMatch reports whether a change to k needs a new conf on every peer.
func MustMatch(k string) bool { return mustMatch[k] }

// Secret reports whether k holds a secret.
func Secret(k string) bool { return k == "header_protection_key" }

// EnvName is the seed env var of a parameter: "s1" -> "AWG_S1".
func EnvName(k string) string { return "AWG_" + strings.ToUpper(k) }

// ValidVersion reports whether v is a known protocol version.
func ValidVersion(v string) bool {
	for _, x := range Versions {
		if x == v {
			return true
		}
	}
	return false
}

// Is3x: the version has the 3.x set (header protection, padding, timers).
func Is3x(v string) bool { return v == "3.0" || v == "3.1" }

// Has2x: the version has H ranges, S3/S4 and I1-I5.
func Has2x(v string) bool { return v == "2.0" || Is3x(v) }

// Clone returns a copy.
func (p Params) Clone() Params {
	c := make(Params, len(p))
	for k, v := range p {
		c[k] = v
	}
	return c
}

// On reports whether a switch is on.
func (p Params) On(k string) bool { return p[k] == "on" }

// Changed returns the keys whose value differs between a and b.
func Changed(a, b Params) []string {
	var out []string
	for _, k := range AllKeys {
		if a[k] != b[k] {
			out = append(out, k)
		}
	}
	return out
}

// QUICInitial is the default I1: a 1200-byte QUIC v1 Initial (long header,
// version 1, 8-byte random DCID, no SCID, no token, length 1182, 4-byte packet
// number, 1178 random bytes of payload).
const QUICInitial = "<b 0xc3><b 0x00000001><b 0x08><r 8><b 0x00><b 0x00><b 0x449e><r 4><r 1178>"

func between(r *rand.Rand, lo, hi int) int { return lo + r.IntN(hi-lo+1) }

func span(lo, hi int) string { return strconv.Itoa(lo) + "-" + strconv.Itoa(hi) }

// NewRand returns a random source for the generators.
func NewRand() *rand.Rand {
	return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

// Generate returns a full parameter set for a version. RandomTrailers is on
// for 3.1 and off otherwise; DisableCookies is off.
func Generate(r *rand.Rand, version string) Params {
	p := Params{"version": version, "random_trailers": "off", "disable_cookies": "off"}
	if version == "3.1" {
		p["random_trailers"] = "on"
	}
	p["jc"] = strconv.Itoa(between(r, 3, 8))
	p["jmin"] = strconv.Itoa(between(r, 40, 80))
	p["jmax"] = strconv.Itoa(between(r, 81, 250))
	drawVersioned(r, p)
	return p
}

// Redraw draws S, H, I and the 3.x set again for p's current version and
// random_trailers. Jc/Jmin/Jmax and the switches stay.
func Redraw(r *rand.Rand, p Params) { drawVersioned(r, p) }

// drawVersioned draws S, H, I and the 3.x set for p's version and
// random_trailers, and clears what the version does not have.
func drawVersioned(r *rand.Rand, p Params) {
	drawS(r, p)
	drawH(r, p, hKeys)
	drawI(p)
	draw3x(r, p, x3Keys)
}

func drawS(r *rand.Rand, p Params) {
	v := p["version"]
	if p.On("random_trailers") && Has2x(v) {
		// Equal values keep every packet-type check on the true type field;
		// 12 is the HeaderProtection floor, <= 20 keeps 1420-MTU packets in 1500.
		s := strconv.Itoa(between(r, 12, 20))
		for _, k := range sKeys {
			p[k] = s
		}
		return
	}
	s1 := between(r, 15, 150)
	s2 := between(r, 15, 150)
	for s1+56 == s2 {
		s2 = between(r, 15, 150)
	}
	p["s1"], p["s2"] = strconv.Itoa(s1), strconv.Itoa(s2)
	switch {
	case Is3x(v):
		p["s3"], p["s4"] = strconv.Itoa(between(r, 12, 55)), strconv.Itoa(between(r, 12, 20))
	case v == "2.0":
		p["s3"], p["s4"] = strconv.Itoa(between(r, 8, 55)), strconv.Itoa(between(r, 4, 20))
	default:
		p["s3"], p["s4"] = "0", "0"
	}
}

// hQuarter is the range of the low end of each H range (2.0+). Each range is
// 50,000,000 wide and stays inside its quarter of 5..2^31-1.
var hQuarter = map[string][2]int{
	"h1": {5, 486870911},
	"h2": {536870912, 1023741823},
	"h3": {1073741824, 1560612735},
	"h4": {1610612736, 2097483647},
}

func drawH(r *rand.Rand, p Params, which []string) {
	if Has2x(p["version"]) {
		for _, k := range which {
			q := hQuarter[k]
			lo := between(r, q[0], q[1])
			p[k] = span(lo, lo+50000000)
		}
		return
	}
	for {
		for _, k := range which {
			p[k] = strconv.Itoa(between(r, 5, 2147483647))
		}
		if len(hErrors(p)) == 0 {
			return
		}
	}
}

func drawI(p Params) {
	if Has2x(p["version"]) {
		p["i1"] = QUICInitial
	} else {
		p["i1"] = ""
	}
	for _, k := range iKeys[1:] {
		p[k] = ""
	}
}

func draw3x(r *rand.Rand, p Params, which []string) {
	if !Is3x(p["version"]) {
		for _, k := range x3Keys {
			delete(p, k)
		}
		return
	}
	want := map[string]bool{}
	for _, k := range which {
		want[k] = true
	}
	// The reject window depends on the other timers, so it follows them.
	for _, k := range timerKeys[:2] {
		if want[k] {
			want["reject_after_time"] = true
		}
	}
	if want["keepalive_timeout"] {
		want["reject_after_time"] = true
	}
	if want["header_protection_key"] {
		p["header_protection_key"] = keys.NewSymmetric()
	}
	if want["content_padding"] {
		if p.On("random_trailers") {
			// Padding suppresses the trailers on the send path and costs ~22%
			// of download speed (docs/awg-performance.md).
			p["content_padding"] = "0"
		} else {
			lo := between(r, 16, 72)
			p["content_padding"] = span(lo, between(r, lo+8, 128))
		}
	}
	if want["rekey_timeout"] {
		lo := between(r, 4, 6)
		p["rekey_timeout"] = span(lo, lo+between(r, 1, 4))
	}
	if want["keepalive_timeout"] {
		lo := between(r, 8, 14)
		p["keepalive_timeout"] = span(lo, lo+between(r, 2, 8))
	}
	if want["rekey_after_time"] {
		lo := between(r, 100, 120)
		p["rekey_after_time"] = span(lo, lo+between(r, 10, 25))
	}
	if want["reject_after_time"] {
		_, rah, _ := parseSpan(p["rekey_after_time"])
		_, kah, _ := parseSpan(p["keepalive_timeout"])
		_, rth, _ := parseSpan(p["rekey_timeout"])
		lo := max(rah+kah+rth+15, 170)
		p["reject_after_time"] = span(lo, lo+between(r, 10, 25))
	}
	if want["max_handshake_attempts"] {
		lo := between(r, 12, 18)
		p["max_handshake_attempts"] = span(lo, lo+between(r, 2, 10))
	}
}

// Transition changes the version deterministically: RandomTrailers is on for
// 3.1 and off for every other version, and S, H, I and the 3.x set are drawn
// again. Jc/Jmin/Jmax and DisableCookies stay.
func Transition(r *rand.Rand, p Params, version string) (Params, error) {
	if !ValidVersion(version) {
		return nil, fmt.Errorf("unknown AWG version %q (use %s)", version, strings.Join(Versions, ", "))
	}
	n := p.Clone()
	n["version"] = version
	n["random_trailers"] = "off"
	if version == "3.1" {
		n["random_trailers"] = "on"
	}
	if n["disable_cookies"] == "" {
		n["disable_cookies"] = "off"
	}
	drawVersioned(r, n)
	return n, nil
}

// Regenerate draws the named keys again (all but version and the switches when
// names is empty). S values are drawn as a group when trailers are on, and the
// reject window follows the other timers.
func Regenerate(r *rand.Rand, p Params, names []string) (Params, error) {
	n := p.Clone()
	if len(names) == 0 {
		n["jc"] = strconv.Itoa(between(r, 3, 8))
		n["jmin"] = strconv.Itoa(between(r, 40, 80))
		n["jmax"] = strconv.Itoa(between(r, 81, 250))
		drawVersioned(r, n)
		return n, nil
	}
	var h, x3 []string
	s := false
	for _, raw := range names {
		k := NormalizeKey(raw)
		switch {
		case k == "version" || k == "random_trailers" || k == "disable_cookies":
			return nil, fmt.Errorf("%s is not drawn; use awgctl awg version or awg set", raw)
		case k == "jc":
			n["jc"] = strconv.Itoa(between(r, 3, 8))
		case k == "jmin" || k == "jmax":
			n["jmin"] = strconv.Itoa(between(r, 40, 80))
			n["jmax"] = strconv.Itoa(between(r, 81, 250))
		case k == "s" || contains(sKeys, k):
			s = true
		case contains(hKeys, k):
			h = append(h, k)
		case contains(iKeys, k):
			drawI(n)
		case contains(x3Keys, k):
			if !Is3x(n["version"]) {
				return nil, fmt.Errorf("%s exists only in AWG 3.0 and 3.1", raw)
			}
			x3 = append(x3, k)
		default:
			return nil, fmt.Errorf("unknown AWG parameter %q", raw)
		}
	}
	if s {
		drawS(r, n)
	}
	if len(h) > 0 {
		drawH(r, n, h)
	}
	if len(x3) > 0 {
		draw3x(r, n, x3)
	}
	return n, nil
}

func contains(list []string, k string) bool {
	for _, x := range list {
		if x == k {
			return true
		}
	}
	return false
}

// NormalizeKey accepts "S1", "awg.s1", "AWG_S1" and conf keys ("HeaderProtectionKey").
func NormalizeKey(raw string) string {
	k := strings.TrimSpace(raw)
	k = strings.TrimPrefix(k, "awg.")
	if strings.HasPrefix(strings.ToUpper(k), "AWG_") {
		k = k[4:]
	}
	if ck := KeyForConf(k); ck != "" {
		return ck
	}
	return strings.ToLower(k)
}

// NormalizeSwitch maps on/off and their usual spellings to "on" or "off".
func NormalizeSwitch(v string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return "on", true
	case "off", "false", "no", "0":
		return "off", true
	}
	return "", false
}

// Set changes several values in one step. "s" sets S1-S4 together. The result
// is not validated here; call Validate on it.
func Set(p Params, kv [][2]string) (Params, error) {
	n := p.Clone()
	for _, pair := range kv {
		k, v := NormalizeKey(pair[0]), strings.TrimSpace(pair[1])
		switch {
		case k == "version":
			return nil, fmt.Errorf("change the version with: awgctl awg version <v>")
		case k == "s":
			for _, s := range sKeys {
				n[s] = v
			}
		case k == "random_trailers" || k == "disable_cookies":
			sw, ok := NormalizeSwitch(v)
			if !ok {
				return nil, fmt.Errorf("%s must be on or off, got %q", k, v)
			}
			n[k] = sw
		case contains(Keys, k):
			if contains(x3Keys, k) && !Is3x(n["version"]) {
				return nil, fmt.Errorf("%s exists only in AWG 3.0 and 3.1", k)
			}
			n[k] = v
		default:
			return nil, fmt.Errorf("unknown AWG parameter %q", pair[0])
		}
	}
	return n, nil
}

// Line is one conf line.
type Line struct{ Key, Value string }

// Lines returns the conf lines in order. Empty values and switches that are
// off are left out ("off" is the default, and an older peer cannot parse the
// key at all).
func Lines(p Params) []Line {
	var out []Line
	for _, k := range Keys {
		v := p[k]
		if v == "" {
			continue
		}
		if (k == "random_trailers" || k == "disable_cookies") && v != "on" {
			continue
		}
		out = append(out, Line{confKey[k], v})
	}
	return out
}

// SortedKeys returns the keys of p in AllKeys order.
func SortedKeys(p Params) []string {
	idx := map[string]int{}
	for i, k := range AllKeys {
		idx[k] = i
	}
	var out []string
	for k := range p {
		out = append(out, k)
	}
	sort.Slice(out, func(a, b int) bool { return idx[out[a]] < idx[out[b]] })
	return out
}
