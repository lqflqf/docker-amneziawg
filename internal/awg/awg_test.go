package awg

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

func seeded(i int) *rand.Rand { return rand.New(rand.NewPCG(uint64(i), uint64(i)*7+1)) }

func mustValid(t *testing.T, p Params) {
	t.Helper()
	if errs, _ := Validate(p); len(errs) > 0 {
		t.Fatalf("invalid set %v: %v", p, errs)
	}
}

func TestGenerateIsValidForEveryVersion(t *testing.T) {
	for _, v := range Versions {
		for i := 0; i < 300; i++ {
			p := Generate(seeded(i), v)
			mustValid(t, p)
			if v == "3.1" && !p.On("random_trailers") {
				t.Fatalf("3.1 must turn RandomTrailers on")
			}
			if v != "3.1" && p.On("random_trailers") {
				t.Fatalf("%s must not turn RandomTrailers on", v)
			}
		}
	}
}

func TestDefaultIs31WithEqualSAndNoPadding(t *testing.T) {
	p := Generate(seeded(1), Default)
	if Default != "3.1" {
		t.Fatalf("default version %s, want 3.1", Default)
	}
	if !(p["s1"] == p["s2"] && p["s2"] == p["s3"] && p["s3"] == p["s4"]) {
		t.Fatalf("trailers need S1=S2=S3=S4: %v", p)
	}
	if n, _ := strconv.Atoi(p["s1"]); n < 12 || n > 20 {
		t.Fatalf("S with trailers must be 12-20, got %d", n)
	}
	if p["content_padding"] != "0" {
		t.Fatalf("content padding must be 0 with trailers, got %q", p["content_padding"])
	}
	if p["i1"] != QUICInitial || strings.Count(p["i1"], "<r 1178>") != 1 {
		t.Fatalf("I1 must be the QUIC Initial with one <r 1178>: %q", p["i1"])
	}
}

func TestTransitionsAreDeterministicAndValid(t *testing.T) {
	for _, from := range Versions {
		for _, to := range Versions {
			p := Generate(seeded(3), from)
			p["disable_cookies"] = "on"
			jc := p["jc"]
			n, err := Transition(seeded(4), p, to)
			if err != nil {
				t.Fatal(err)
			}
			mustValid(t, n)
			if want := map[bool]string{true: "on", false: "off"}[to == "3.1"]; n["random_trailers"] != want {
				t.Errorf("%s->%s: random_trailers %s, want %s", from, to, n["random_trailers"], want)
			}
			if n["jc"] != jc || n["disable_cookies"] != "on" {
				t.Errorf("%s->%s: Jc and DisableCookies must stay", from, to)
			}
			if !Is3x(to) && n["header_protection_key"] != "" {
				t.Errorf("%s->%s: 3.x keys must go", from, to)
			}
			if to == "1.5" && (n["i1"] != "" || n["s3"] != "0" || strings.Contains(n["h1"], "-")) {
				t.Errorf("->1.5 must have no I1, S3=0 and integer H: %v", n)
			}
		}
	}
	if _, err := Transition(seeded(1), Generate(seeded(1), "2.0"), "4.0"); err == nil {
		t.Fatal("unknown version accepted")
	}
}

func TestSetSWithTrailersNeedsAllFour(t *testing.T) {
	p := Generate(seeded(5), "3.1")
	s, _ := strconv.Atoi(p["s1"])
	one, err := Set(p, [][2]string{{"s1", strconv.Itoa(s + 1)}})
	if err != nil {
		t.Fatal(err)
	}
	if errs, _ := Validate(one); len(errs) == 0 {
		t.Fatal("S1 alone with trailers on must fail the equal-S rule")
	}
	all, err := Set(p, [][2]string{{"s", "18"}})
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, all)
	if all["s4"] != "18" {
		t.Fatalf("s=18 must set S4: %v", all)
	}
}

func TestSetRefusesVersionAndUnknownKeys(t *testing.T) {
	p := Generate(seeded(5), "2.0")
	for _, kv := range [][2]string{{"version", "3.0"}, {"nope", "1"}, {"header_protection_key", "x"}, {"random_trailers", "maybe"}} {
		if _, err := Set(p, [][2]string{kv}); err == nil {
			t.Errorf("Set(%v) accepted", kv)
		}
	}
	n, err := Set(p, [][2]string{{"AWG_JC", "5"}, {"awg.jmax", "300"}, {"DisableCookies", "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if n["jc"] != "5" || n["jmax"] != "300" || n["disable_cookies"] != "on" {
		t.Fatalf("key spellings not normalized: %v", n)
	}
}

func TestValidateRules(t *testing.T) {
	base := Generate(seeded(9), "3.0")
	cases := []struct {
		name string
		kv   [][2]string
	}{
		{"S below 12 under 3.x", [][2]string{{"s1", "8"}}},
		{"S1+56 = S2", [][2]string{{"s1", "20"}, {"s2", "76"}}},
		{"overlapping H", [][2]string{{"h2", base["h1"]}}},
		{"Jmin >= Jmax", [][2]string{{"jmin", "300"}, {"jmax", "200"}}},
		{"Jc above 128", [][2]string{{"jc", "200"}}},
		{"bad signature", [][2]string{{"i1", "<x 5>"}}},
		{"odd hex", [][2]string{{"i1", "<b 0xabc>"}}},
		{"I2 without I1", [][2]string{{"i1", ""}, {"i2", "<r 10>"}}},
		{"padding with trailers", [][2]string{{"random_trailers", "on"}, {"s", "16"}, {"content_padding", "16-32"}}},
		{"reject window", [][2]string{{"reject_after_time", "10-20"}}},
		{"bad header key", [][2]string{{"header_protection_key", "abc"}}},
	}
	for _, c := range cases {
		p, err := Set(base, c.kv)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if errs, _ := Validate(p); len(errs) == 0 {
			t.Errorf("%s: accepted", c.name)
		}
	}
	p15 := Generate(seeded(2), "1.5")
	p15["random_trailers"] = "on"
	if errs, _ := Validate(p15); len(errs) == 0 {
		t.Error("RandomTrailers with 1.5 accepted")
	}
	// The S maximums are conventions: a warning, not an error.
	big, _ := Set(Generate(seeded(2), "2.0"), [][2]string{{"s4", "30"}})
	errs, warns := Validate(big)
	if len(errs) > 0 || len(warns) == 0 {
		t.Errorf("S4=30: errs %v warns %v; want a warning only", errs, warns)
	}
}

func TestContentPaddingAndTimersMayDifferPerEnd(t *testing.T) {
	for _, k := range []string{"content_padding", "rekey_after_time", "jc", "disable_cookies"} {
		if MustMatch(k) {
			t.Errorf("%s must not force new peer confs", k)
		}
	}
	for _, k := range []string{"s1", "h4", "i1", "header_protection_key", "random_trailers"} {
		if !MustMatch(k) {
			t.Errorf("%s must match on every peer", k)
		}
	}
}

func TestCustomSignaturesAreAccepted(t *testing.T) {
	for _, s := range []string{
		"<b 0x1234><r 16><t>",
		"<b 0xdeadbeef> <rc 8> <rd 4>",
		"<r 1000><r 178>",
	} {
		if err := CheckSignature(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestRegenerateKeepsTrailerRule(t *testing.T) {
	p := Generate(seeded(11), "3.1")
	for i := 0; i < 50; i++ {
		n, err := Regenerate(seeded(i), p, []string{"s2"})
		if err != nil {
			t.Fatal(err)
		}
		mustValid(t, n)
	}
	if _, err := Regenerate(seeded(1), Generate(seeded(1), "2.0"), []string{"rekey_timeout"}); err == nil {
		t.Fatal("3.x key regenerated under 2.0")
	}
}

func TestLinesSkipOffSwitchesAndEmptyValues(t *testing.T) {
	p := Generate(seeded(1), "2.0")
	for _, l := range Lines(p) {
		if l.Value == "" || l.Key == "RandomTrailers" || l.Key == "DisableCookies" || l.Key == "I2" {
			t.Fatalf("unexpected line %v", l)
		}
	}
	p["disable_cookies"] = "on"
	found := false
	for _, l := range Lines(p) {
		found = found || l.Key == "DisableCookies" && l.Value == "on"
	}
	if !found {
		t.Fatal("DisableCookies = on not written")
	}
}

func FuzzCheckSignature(f *testing.F) {
	f.Add(QUICInitial)
	f.Add("<b 0x00><t>")
	f.Fuzz(func(t *testing.T, s string) { _ = CheckSignature(s) })
}

func with(p Params, kv ...string) Params {
	n := p.Clone()
	for i := 0; i < len(kv); i += 2 {
		n[kv[i]] = kv[i+1]
	}
	return n
}

func TestKeyNamesRoundTrip(t *testing.T) {
	for _, k := range Keys {
		for _, raw := range []string{k, strings.ToUpper(k), "awg." + k, EnvName(k), ConfKey(k), " " + ConfKey(k) + " "} {
			if got := NormalizeKey(raw); got != k {
				t.Errorf("NormalizeKey(%q) = %q, want %q", raw, got, k)
			}
		}
		if KeyForConf(strings.ToLower(ConfKey(k))) != k {
			t.Errorf("KeyForConf is not case-insensitive for %s", ConfKey(k))
		}
		if Secret(k) != (k == "header_protection_key") {
			t.Errorf("Secret(%s) = %v", k, Secret(k))
		}
	}
	if EnvName("s1") != "AWG_S1" || KeyForConf("Nope") != "" || NormalizeKey("AWG_S") != "s" {
		t.Error("env name or unknown conf key")
	}
}

func TestNormalizeSwitch(t *testing.T) {
	for in, want := range map[string]string{"on": "on", " TRUE ": "on", "yes": "on", "1": "on", "Off": "off", "false": "off", "no": "off", "0": "off", "maybe": "", "": ""} {
		got, ok := NormalizeSwitch(in)
		if got != want || ok != (want != "") {
			t.Errorf("NormalizeSwitch(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}

func TestChangedAndSortedKeys(t *testing.T) {
	a := Generate(seeded(1), "3.0")
	b := with(a, "version", "3.1", "s2", "99", "disable_cookies", "on", "unknown", "x")
	delete(b, "jc")
	if got := strings.Join(Changed(a, b), ","); got != "version,jc,s2,disable_cookies" {
		t.Errorf("Changed = %s", got)
	}
	if len(Changed(a, a.Clone())) != 0 {
		t.Error("a clone differs")
	}
	got := SortedKeys(Params{"disable_cookies": "off", "s1": "1", "version": "2.0", "h1": "5", "jc": "3"})
	if strings.Join(got, ",") != "version,jc,s1,h1,disable_cookies" {
		t.Errorf("SortedKeys = %v", got)
	}
}

func TestNewRandAndRedraw(t *testing.T) {
	for _, v := range Versions {
		p := Generate(NewRand(), v)
		mustValid(t, p)
		n := p.Clone()
		n["disable_cookies"] = "on"
		Redraw(seeded(7), n)
		mustValid(t, n)
		for _, k := range []string{"version", "jc", "jmin", "jmax", "random_trailers"} {
			if n[k] != p[k] {
				t.Errorf("%s: Redraw changed %s from %q to %q", v, k, p[k], n[k])
			}
		}
		if n["disable_cookies"] != "on" || n["h1"] == p["h1"] {
			t.Errorf("%s: Redraw must keep the switches and draw H again: %v -> %v", v, p, n)
		}
	}
}

func TestTransitionFillsMissingDisableCookies(t *testing.T) {
	p := Generate(seeded(1), "2.0")
	delete(p, "disable_cookies")
	n, err := Transition(seeded(2), p, "3.0")
	if err != nil {
		t.Fatal(err)
	}
	mustValid(t, n)
	if n["disable_cookies"] != "off" {
		t.Fatalf("disable_cookies = %q, want off", n["disable_cookies"])
	}
	if _, err := Transition(seeded(1), p, "4.0"); err == nil || err.Error() != `unknown AWG version "4.0" (use 1.5, 2.0, 3.0, 3.1)` {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestRegenerate(t *testing.T) {
	for _, v := range Versions {
		p := Generate(seeded(20), v)
		p["disable_cookies"] = "on"
		all, err := Regenerate(seeded(21), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		mustValid(t, all)
		if all["version"] != v || all["disable_cookies"] != "on" || all["random_trailers"] != p["random_trailers"] {
			t.Errorf("%s: Regenerate() changed the version or a switch: %v", v, all)
		}
		if all["h1"] == p["h1"] {
			t.Errorf("%s: Regenerate() did not draw H1 again", v)
		}
		names := []string{"jc", "Jmin", "AWG_S3", "h2", "I1"}
		if Is3x(v) {
			names = append(names, "rekey_timeout", "keepalive_timeout", "content_padding", "rekey_after_time",
				"max_handshake_attempts", "HeaderProtectionKey")
		}
		for i := 0; i < 30; i++ {
			n, err := Regenerate(seeded(i), p, names)
			if err != nil {
				t.Fatalf("%s %v: %v", v, names, err)
			}
			mustValid(t, n)
			if n["h1"] != p["h1"] || n["h3"] != p["h3"] {
				t.Fatalf("%s: H1 and H3 were not named but changed", v)
			}
		}
		if Is3x(v) {
			n, _ := Regenerate(seeded(3), p, []string{"rekey_timeout"})
			if n["reject_after_time"] == p["reject_after_time"] && n["rekey_timeout"] != p["rekey_timeout"] {
				t.Errorf("%s: the reject window does not follow the timers", v)
			}
			if n["header_protection_key"] != p["header_protection_key"] {
				t.Errorf("%s: a key that was not named changed", v)
			}
		}
	}
	p := Generate(seeded(1), "3.1")
	for _, c := range []struct{ name, want string }{
		{"version", "version is not drawn; use awgctl awg version or awg set"},
		{"RandomTrailers", "RandomTrailers is not drawn"},
		{"disable_cookies", "disable_cookies is not drawn"},
		{"nope", `unknown AWG parameter "nope"`},
	} {
		if _, err := Regenerate(seeded(1), p, []string{c.name}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Regenerate(%s): %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := Regenerate(seeded(1), Generate(seeded(1), "1.5"), []string{"content_padding"}); err == nil ||
		err.Error() != "content_padding exists only in AWG 3.0 and 3.1" {
		t.Errorf("3.x key under 1.5: %v", err)
	}
}

func TestValidateErrorMessages(t *testing.T) {
	p30 := with(Generate(seeded(9), "3.0"), "s1", "30", "s2", "40", "s3", "20", "s4", "16")
	p20 := Generate(seeded(9), "2.0")
	p15 := Generate(seeded(9), "1.5")
	cases := []struct {
		name string
		p    Params
		want string
	}{
		{"version", with(p30, "version", "2.5"), `version "2.5" is not one of 1.5, 2.0, 3.0, 3.1`},
		{"switch", with(p30, "disable_cookies", "yes"), `DisableCookies must be on or off, got "yes"`},
		{"newline", with(p30, "i2", "<r 5>\n<t>"), "I2 must be one line"},
		{"jc text", with(p30, "jc", "x"), `Jc ("x") must be a non-negative integer`},
		{"jc leading zero", with(p30, "jc", "05"), `Jc ("05") must be a non-negative integer`},
		{"jmax above 1280", with(p30, "jmin", "40", "jmax", "1281"), "Jmin (40) < Jmax (1281) <= 1280 is required"},
		{"s negative", with(p30, "s2", "-1"), `S2 ("-1") must be a non-negative integer`},
		{"s below 12", with(p30, "s4", "11"), "S4 (11) must be >= 12 under AWG 3.0"},
		{"1.5 S3", with(p15, "s3", "5"), "AWG 1.5 has no S3/S4: they must be 0 (got 5, 0)"},
		{"1.5 I1", with(p15, "i1", "<r 5>"), "AWG 1.5 has no I1"},
		{"1.5 3.x key", with(p15, "rekey_timeout", "5-6"), "RekeyTimeout exists only in AWG 3.0 and 3.1"},
		{"1.5 H range", with(p15, "h1", "10-20"), "H1 (10-20): AWG 1.5 takes one integer, not a range"},
		{"H below 5", with(p20, "h1", "4-10"), "H1 (4-10) must be within 5-4294967295"},
		{"H above 32 bits", with(p20, "h4", "4294967290-4294967296"), "H4 (4294967290-4294967296) must be within 5-4294967295"},
		{"H reversed", with(p20, "h2", "20-10"), `H2: "20-10" is not a lo-hi range with lo <= hi`},
		{"H text", with(p20, "h3", "x"), `H3: "x" is not a number or a lo-hi range`},
		{"H negative", with(p20, "h3", "-5"), `H3: "-5" is not a number or a lo-hi range`},
		{"H overlap", with(p20, "h1", "10-20", "h2", "20-30"), "H1 (10-20) and H2 (20-30) overlap"},
		{"empty I", with(p20, "i3", " "), "I3 is empty"},
		{"tag length 0", with(p20, "i1", "<r 0>"), "I1 <r 0>: the length must be 1-65535"},
		{"tag length big", with(p20, "i1", "<rd 65536>"), "I1 <rd 65536>: the length must be 1-65535"},
		{"long unknown tag", with(p20, "i1", "<b 0x00><unknown tag that is long>"), `I1 unknown tag at "<unknown tag that is…"`},
		{"short unknown tag", with(p20, "i1", "<x>"), `I1 unknown tag at "<x>"`},
		{"header key", with(p30, "header_protection_key", ""), "HeaderProtectionKey must be a base64 32-byte key"},
		{"padding text", with(p30, "content_padding", "x"), `ContentPaddingAddition: "x" is not a number`},
		{"padding above 1000", with(p30, "content_padding", "16-1001"), "ContentPaddingAddition (16-1001) must be within 0-1000"},
		{"timer text", with(p30, "keepalive_timeout", "x"), `KeepaliveTimeout: "x" is not a number`},
		{"rekey after reject", with(p30, "rekey_after_time", "100-500"), "RekeyAfterTime (100-500) upper bound must be less than"},
		{"handshake attempts", with(p30, "max_handshake_attempts", "0-5"), "MaxHandshakeAttempts (0-5) lower bound must be >= 1"},
	}
	for _, c := range cases {
		errs, _ := Validate(c.p)
		var msgs []string
		for _, err := range errs {
			msgs = append(msgs, err.Error())
		}
		if !strings.Contains(strings.Join(msgs, "\n"), c.want) {
			t.Errorf("%s: errors %q, want %q", c.name, msgs, c.want)
		}
	}
	if errs, _ := Validate(with(p30, "version", "")); len(errs) != 1 {
		t.Errorf("an unknown version must stop the checks: %v", errs)
	}
}

func TestValidateWarnsAboveSMaximums(t *testing.T) {
	p := with(Generate(seeded(9), "2.0"), "s1", "1133", "s2", "1190", "s3", "65", "s4", "33")
	errs, warns := Validate(p)
	if len(errs) > 0 {
		t.Fatalf("S maximums must not be errors: %v", errs)
	}
	got := strings.Join(warns, "\n")
	for _, want := range []string{"S1 (1133) is above the conventional maximum 1132", "S2 (1190)", "S3 (65)", "S4 (33) is above the conventional maximum 32", "S4 (33) is above 20"} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings %q, want %q", got, want)
		}
	}
}
