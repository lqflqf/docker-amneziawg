package systest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.conf")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(f *Fake, name string, args ...string) (string, string, error) {
	out, errb, err := f.Run(context.Background(), name, args, nil, nil)
	return string(out), string(errb), err
}

func TestStrip(t *testing.T) {
	f := New()
	p := write(t, "[Interface]\nAddress = 10.0.0.1/24\nmtu=1280\nPrivateKey = k\nPostUp = x\n[Peer]\nPublicKey = p\n")
	out, _, err := run(f, "awg-quick", "strip", p)
	if err != nil {
		t.Fatal(err)
	}
	if out != "[Interface]\nPrivateKey = k\n[Peer]\nPublicKey = p\n" {
		t.Errorf("strip = %q", out)
	}
}

func TestSetconf(t *testing.T) {
	f := New()
	for _, c := range []struct{ text, want string }{
		{"PrivateKey = k\n", "Unable to modify interface"},
		{"Bogus = 1\n", "Line unrecognized"},
		{"Address = 10.0.0.1/24\n", "Line unrecognized"},
	} {
		_, errb, err := run(f, "awg", "setconf", "x", write(t, c.text))
		if err == nil || !strings.Contains(errb, c.want) {
			t.Errorf("%q: err %v, stderr %q", c.text, err, errb)
		}
	}
}

func TestDnsmasqTest(t *testing.T) {
	f := New()
	if _, errb, err := run(f, "dnsmasq", "--test", "-C", write(t, "no-resolv\n")); err != nil || !strings.Contains(errb, "OK") {
		t.Errorf("good conf: %v %q", err, errb)
	}
	if _, _, err := run(f, "dnsmasq", "--test", "-C", write(t, "bogus-option\n")); err == nil {
		t.Error("bad conf must fail")
	}
}

func TestShowAndSyncconf(t *testing.T) {
	f := New()
	if _, _, err := run(f, "awg", "show", "wg0"); err == nil {
		t.Error("show wg0 must fail when down")
	}
	if out, _, _ := run(f, "awg", "show", "interfaces"); strings.TrimSpace(out) != "" {
		t.Errorf("interfaces when down = %q", out)
	}
	if _, _, err := run(f, "awg", "show", "wg0", "dump"); err == nil {
		t.Error("dump must fail when down")
	}

	f.Up = true
	if out, _, err := run(f, "awg", "show", "wg0"); err != nil || !strings.Contains(out, "wg0") {
		t.Errorf("show wg0 = %q, %v", out, err)
	}
	if out, _, _ := run(f, "awg", "show", "interfaces"); out != "wg0\n" {
		t.Errorf("interfaces when up = %q", out)
	}

	f.Ghost = "ghost"
	if _, _, err := run(f, "awg", "syncconf", "wg0", write(t, "[Peer]\nPublicKey = a\n[Peer]\nPublicKey= b\n")); err != nil {
		t.Fatal(err)
	}
	if out, _, _ := run(f, "awg", "show", "wg0", "peers"); out != "a\nb\nghost\n" {
		t.Errorf("peers = %q", out)
	}
	out, _, err := run(f, "awg", "show", "wg0", "dump")
	if err != nil || len(strings.Split(strings.TrimSpace(out), "\n")) != 4 || !strings.HasPrefix(out, "priv\tpub") {
		t.Errorf("dump = %q, %v", out, err)
	}
}

func TestOtherCommands(t *testing.T) {
	f := New()
	if _, _, err := run(f, "s6-svc", "-r", "/run/service/svc-dnsmasq"); err != nil {
		t.Error(err)
	}
	if out, _, err := run(f, "dnsmasq", "--version"); err != nil || out != "dnsmasq v0-test\n" {
		t.Errorf("version = %q, %v", out, err)
	}
	if _, errb, err := run(f, "rm", "-rf", "/"); err == nil || !strings.Contains(errb, "unexpected command rm -rf /") {
		t.Errorf("unexpected command: %v %q", err, errb)
	}
}

func TestFailCalledReset(t *testing.T) {
	f := New()
	f.Up = true
	f.Fail["awg show"] = "boom"
	if _, errb, err := run(f, "awg", "show", "wg0"); err == nil || errb != "boom" {
		t.Errorf("Fail: %v %q", err, errb)
	}
	if !f.Called("awg show wg0") || f.Called("awg syncconf") {
		t.Errorf("Called is wrong: %v", f.Calls)
	}
	f.Reset()
	if f.Called("awg") || len(f.Calls) != 0 {
		t.Errorf("Reset kept %v", f.Calls)
	}
}
