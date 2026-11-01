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
