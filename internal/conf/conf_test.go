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
