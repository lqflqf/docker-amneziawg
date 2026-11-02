package conf

import (
	"strings"
	"testing"
)

const sample = `[Interface]
Address = 10.0.0.2
PrivateKey = abc
I1 = <b 0xc3><r 8>
# a comment

[Peer]
PublicKey = srv
AllowedIPs = 0.0.0.0/0, ::/0
`

func TestParseKeepsEqualsInValues(t *testing.T) {
	f, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Interface().Get("I1"); got != "<b 0xc3><r 8>" {
		t.Fatalf("I1 = %q", got)
	}
	if len(f.Peers()) != 1 || f.Peers()[0].Get("publickey") != "srv" {
		t.Fatal("peer not parsed")
	}
	if _, err := Parse("Key = outside\n"); err == nil {
		t.Fatal("a line outside a section was accepted")
	}
}

func TestWriterRoundTripAndCheck(t *testing.T) {
	f := &File{Sections: []Section{{Name: "Interface", Comments: []string{"x"}}}}
	f.Sections[0].Add("PrivateKey", "k")
	f.Sections[0].Add("Empty", "")
	out := f.String()
	if strings.Contains(out, "Empty") {
		t.Fatal("empty value written")
	}
	g, err := Parse(out)
	if err != nil || g.Interface().Get("PrivateKey") != "k" {
		t.Fatalf("round trip: %v %q", err, out)
	}
	f.Sections[0].Add("PostUp", "a\nb")
	if f.Check() == nil {
		t.Fatal("a newline in a value passed Check")
	}
}

func TestDiff(t *testing.T) {
	a, _ := Parse(sample)
	b, _ := Parse(strings.Replace(sample, "Address = 10.0.0.2", "Address = 10.0.0.2/32", 1))
	norm := func(k, v string) string {
		if k == "address" {
			return strings.TrimSuffix(v, "/32")
		}
		return v
	}
	if d := Diff(a, b, norm); len(d) != 0 {
		t.Fatalf("normalized confs differ: %v", d)
	}
	c, _ := Parse(strings.Replace(sample, "<r 8>", "<r 9>", 1))
	if d := Diff(a, c, norm); len(d) != 1 || d[0] != "Interface.i1" {
		t.Fatalf("diff = %v", d)
	}
}

func TestSectionGetAll(t *testing.T) {
	f, err := Parse("[Peer]\nAllowedIPs = 10.0.0.2/32\nallowedips = 10.0.0.3/32\n")
	if err != nil {
		t.Fatal(err)
	}
	p := f.Peers()[0]
	if got := p.All("AllowedIPs"); len(got) != 2 || got[1] != "10.0.0.3/32" {
		t.Fatalf("All = %v", got)
	}
	if p.Get("Endpoint") != "" || p.All("Endpoint") != nil {
		t.Fatal("a missing key returned a value")
	}
	if f.Interface() != nil {
		t.Fatal("Interface found in a conf without [Interface]")
	}
}

func TestCheck(t *testing.T) {
	for name, c := range map[string]struct {
		s    Section
		want string
	}{
		"good":             {Section{Name: "Interface", Comments: []string{"ok"}, Lines: []KV{{"MTU", "1280"}}}, ""},
		"comment newline":  {Section{Name: "Peer", Comments: []string{"a\rb"}}, "[Peer]: a comment contains a newline"},
		"empty key":        {Section{Name: "Peer", Lines: []KV{{"", "x"}}}, "[Peer] : a key or value contains a newline"},
		"newline in a key": {Section{Name: "Peer", Lines: []KV{{"A\nB", "x"}}}, "a key or value contains a newline"},
	} {
		f := &File{Sections: []Section{c.s}}
		err := f.Check()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestStringSeparatesSections(t *testing.T) {
	f := &File{Sections: []Section{
		{Name: "Interface", Lines: []KV{{"PrivateKey", "k"}}},
		{Name: "Peer", Comments: []string{"laptop"}, Lines: []KV{{"PublicKey", "p"}}},
	}}
	want := "[Interface]\nPrivateKey = k\n\n[Peer]\n# laptop\nPublicKey = p\n"
	if got := f.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseRejectsLineWithoutEquals(t *testing.T) {
	_, err := Parse("[Interface]\nPrivateKey = k\njunk\n")
	if err == nil || !strings.Contains(err.Error(), `line 3: no '=' in "junk"`) {
		t.Fatalf("got %v", err)
	}
}

func TestDiffSectionsAndPeers(t *testing.T) {
	parse := func(s string) *File {
		t.Helper()
		f, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	for name, c := range map[string]struct {
		a, b string
		want string
	}{
		"same":             {"[Peer]\nPublicKey = a\n", "[Peer]\nPublicKey = a\n", ""},
		"key only in b":    {"[Interface]\nMTU = 1\n", "[Interface]\nMTU = 1\nDNS = x\n", "Interface.dns"},
		"interface in a":   {"[Interface]\nMTU = 1\n", "[Peer]\nPublicKey = a\n", "Interface Peer.PublicKey"},
		"interface in b":   {"", "[Interface]\nMTU = 1\n", "Interface"},
		"peer only in a":   {"[Peer]\nPublicKey = a\n[Peer]\nPublicKey = b\n", "[Peer]\nPublicKey = a\n", "Peer.PublicKey"},
		"peer only in b":   {"[Peer]\nPublicKey = a\n", "[Peer]\nPublicKey = b\n", "Peer.PublicKey"},
		"peer value":       {"[Peer]\nPublicKey = a\nAllowedIPs = 1\n", "[Peer]\nPublicKey = a\nAllowedIPs = 2\n", "Peer.allowedips"},
		"nil normalize ok": {"[Interface]\nMTU = 1\nMTU = 2\n", "[Interface]\nMTU = 2\nMTU = 1\n", ""},
	} {
		got := strings.Join(Diff(parse(c.a), parse(c.b), nil), " ")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
