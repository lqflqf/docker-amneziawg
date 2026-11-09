package awg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/keys"
)

// parseSpan reads "lo-hi" or a single integer n (= n-n).
func parseSpan(s string) (lo, hi int, err error) {
	a, b, isSpan := strings.Cut(s, "-")
	lo, err = strconv.Atoi(a)
	if err != nil || lo < 0 {
		return 0, 0, fmt.Errorf("%q is not a number or a lo-hi range", s)
	}
	hi = lo
	if isSpan {
		hi, err = strconv.Atoi(b)
		if err != nil || hi < lo {
			return 0, 0, fmt.Errorf("%q is not a lo-hi range with lo <= hi", s)
		}
	}
	return lo, hi, nil
}

func atoi(p Params, k string) (int, error) {
	v := p[k]
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || (len(v) > 1 && v[0] == '0') {
		return 0, fmt.Errorf("%s (%q) must be a non-negative integer", ConfKey(k), v)
	}
	return n, nil
}

// hErrors checks H1-H4: all >= 5, within 32 bits, and no two equal (1.5) or
// overlapping (2.0+).
func hErrors(p Params) []error {
	var errs []error
	type rng struct {
		k      string
		lo, hi int
	}
	var rs []rng
	for _, k := range hKeys {
		lo, hi, err := parseSpan(p[k])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", ConfKey(k), err))
			continue
		}
		if lo < 5 || hi > 4294967295 {
			errs = append(errs, fmt.Errorf("%s (%s) must be within 5-4294967295", ConfKey(k), p[k]))
		}
		if !Has2x(p["version"]) && lo != hi {
			errs = append(errs, fmt.Errorf("%s (%s): AWG 1.5 takes one integer, not a range", ConfKey(k), p[k]))
		}
		rs = append(rs, rng{k, lo, hi})
	}
	for i := 0; i < len(rs); i++ {
		for j := i + 1; j < len(rs); j++ {
			if rs[i].lo <= rs[j].hi && rs[j].lo <= rs[i].hi {
				errs = append(errs, fmt.Errorf("%s (%s) and %s (%s) overlap; H1-H4 must be distinct",
					ConfKey(rs[i].k), p[rs[i].k], ConfKey(rs[j].k), p[rs[j].k]))
			}
		}
	}
	return errs
}

var (
	tagRe = regexp.MustCompile(`^<(b 0x[0-9A-Fa-f]+|r [0-9]+|rd [0-9]+|rc [0-9]+|t)>`)
)

// CheckSignature checks the custom protocol signature syntax of I1-I5: a
// sequence of <b 0xHEX>, <r N>, <rd N>, <rc N> and <t> tags. Any protocol
// shape is allowed.
func CheckSignature(s string) error {
	if strings.ContainsAny(s, "\n\r") {
		return fmt.Errorf("must be one line")
	}
	rest := strings.TrimSpace(s)
	if rest == "" {
		return fmt.Errorf("is empty")
	}
	for rest != "" {
		m := tagRe.FindString(rest)
		if m == "" {
			return fmt.Errorf("unknown tag at %q (use <b 0xHEX>, <r N>, <rd N>, <rc N>, <t>)", trunc(rest))
		}
		body := m[1 : len(m)-1]
		switch {
		case strings.HasPrefix(body, "b 0x"):
			if (len(body)-4)%2 != 0 {
				return fmt.Errorf("%s: the hex string needs an even number of digits", m)
			}
		case body != "t":
			n, err := strconv.Atoi(body[strings.IndexByte(body, ' ')+1:])
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("%s: the length must be 1-65535", m)
			}
		}
		rest = strings.TrimLeft(rest[len(m):], " ")
	}
	return nil
}

func trunc(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}

// Validate checks every rule. Errors make a set unusable (amneziawg-go or the
// peers would reject it); warnings are conventions.
func Validate(p Params) (errs []error, warns []string) {
	e := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	v := p["version"]
	if !ValidVersion(v) {
		e("version %q is not one of %s", v, strings.Join(Versions, ", "))
		return errs, nil
	}
	for _, k := range Switches {
		if p[k] != "on" && p[k] != "off" {
			e("%s must be on or off, got %q", ConfKey(k), p[k])
		}
	}
	for _, k := range Keys {
		if strings.ContainsAny(p[k], "\n\r") {
			e("%s must be one line", ConfKey(k))
		}
	}

	// Junk packets
	jc, err1 := atoi(p, "jc")
	jmin, err2 := atoi(p, "jmin")
	jmax, err3 := atoi(p, "jmax")
	for _, err := range []error{err1, err2, err3} {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err1 == nil && jc > 128 {
		e("Jc (%d) must be 0-128", jc)
	}
	if err2 == nil && err3 == nil && !(jmin < jmax && jmax <= 1280) {
		e("Jmin (%d) < Jmax (%d) <= 1280 is required", jmin, jmax)
	}

	// Padding
	var s [4]int
	sOK := true
	for i, k := range sKeys {
		n, err := atoi(p, k)
		if err != nil {
			errs = append(errs, err)
			sOK = false
			continue
		}
		s[i] = n
	}
	if sOK {
		if s[0]+56 == s[1] {
			e("S1 + 56 (%d) must not equal S2: the init and response messages would have the same size", s[0]+56)
		}
		switch {
		case v == "1.5":
			if s[2] != 0 || s[3] != 0 {
				e("AWG 1.5 has no S3/S4: they must be 0 (got %d, %d)", s[2], s[3])
			}
		case Is3x(v):
			for i, n := range s {
				if n < 12 {
					e("S%d (%d) must be >= 12 under AWG %s: the header protection nonce is read from the padding", i+1, n, v)
				}
			}
		}
		if p.On(RandomTrailers) && Has2x(v) && !(s[0] == s[1] && s[1] == s[2] && s[2] == s[3]) {
			e("RandomTrailers needs S1 = S2 = S3 = S4 (got %d, %d, %d, %d); set them together with: awgctl awg set s=<12-20>",
				s[0], s[1], s[2], s[3])
		}
		for i, maxS := range []int{1132, 1188, 64, 32} {
			if s[i] > maxS {
				warns = append(warns, fmt.Sprintf("S%d (%d) is above the conventional maximum %d", i+1, s[i], maxS))
			}
		}
		if s[3] > 20 {
			warns = append(warns, fmt.Sprintf("S4 (%d) is above 20: every transport packet carries it, and full-size packets can fragment (docs/mtu.md)", s[3]))
		}
	}

	errs = append(errs, hErrors(p)...)

	// Signature packets
	if Has2x(v) {
		for i, k := range iKeys {
			if p[k] == "" {
				continue
			}
			if i > 0 && p["i1"] == "" {
				e("%s needs I1", ConfKey(k))
			}
			if err := CheckSignature(p[k]); err != nil {
				e("%s %v", ConfKey(k), err)
			}
		}
	} else {
		for _, k := range iKeys {
			if p[k] != "" {
				e("AWG 1.5 has no %s", ConfKey(k))
			}
		}
	}

	if v == "1.5" && p.On(RandomTrailers) {
		e("RandomTrailers is not supported with AWG 1.5")
	}

	// 3.x set
	if Is3x(v) {
		if !keys.Valid(p["header_protection_key"]) {
			e("HeaderProtectionKey must be a base64 32-byte key")
		}
		cp := p["content_padding"]
		lo, hi, err := parseSpan(cp)
		if err != nil {
			e("ContentPaddingAddition: %v", err)
		} else if p.On(RandomTrailers) && hi != 0 {
			e("ContentPaddingAddition (%s) must be 0 with RandomTrailers: padding suppresses the trailers and costs ~22%% of download speed", cp)
		} else if lo < 0 || hi > 1000 {
			e("ContentPaddingAddition (%s) must be within 0-1000", cp)
		}
		var t = map[string][2]int{}
		for _, k := range timerKeys {
			lo, hi, err := parseSpan(p[k])
			if err != nil {
				e("%s: %v", ConfKey(k), err)
				continue
			}
			t[k] = [2]int{lo, hi}
		}
		if len(t) == len(timerKeys) {
			if t["reject_after_time"][0] <= t["keepalive_timeout"][0]+t["rekey_timeout"][0] {
				e("RejectAfterTime (%s) lower bound must be greater than KeepaliveTimeout.lo + RekeyTimeout.lo", p["reject_after_time"])
			}
			if t["rekey_after_time"][1] >= t["reject_after_time"][0] {
				e("RekeyAfterTime (%s) upper bound must be less than the RejectAfterTime lower bound (%s)", p["rekey_after_time"], p["reject_after_time"])
			}
			if t["max_handshake_attempts"][0] < 1 {
				e("MaxHandshakeAttempts (%s) lower bound must be >= 1", p["max_handshake_attempts"])
			}
		}
	} else {
		for _, k := range x3Keys {
			if p[k] != "" {
				e("%s exists only in AWG 3.0 and 3.1", ConfKey(k))
			}
		}
	}
	return errs, warns
}
