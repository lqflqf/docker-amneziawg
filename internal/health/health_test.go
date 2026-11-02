package health

import (
	"context"
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/apply"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
	"github.com/lqflqf/docker-amneziawg/internal/sys/systest"
)

func write(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheck(t *testing.T) {
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, c := range []struct {
		name    string
		down    bool
		state   *apply.State
		dns     string
		address string
		want    string
	}{
		{name: "down", down: true, want: "tunnel wg0 is down"},
		{name: "no state", want: "no apply state"},
		{name: "pending without time", state: &apply.State{Committed: 2, Applied: 1},
			want: "a saved change is not applied; run awgctl apply"},
		{name: "pending too long", state: &apply.State{Committed: 2, Applied: 1, PendingSince: old, ApplyError: "boom"},
			want: "a saved change is not applied: boom; run"},
		{name: "pending in grace", state: &apply.State{Committed: 2, Applied: 1, PendingSince: now},
			dns: "invalid", want: "dnsmasq has no valid config"},
		{name: "dns invalid", dns: "invalid\n", want: "dnsmasq has no valid config"},
		{name: "dns missing", want: "dnsmasq is not running (state: none)"},
		{name: "dns other", dns: "starting", want: "dnsmasq is not running (state: starting)"},
		{name: "no address", dns: "running", want: "no tunnel address"},
		{name: "bad address", dns: "running", address: "not-an-ip", want: "no tunnel address"},
		{name: "address not on wg0", dns: "running", address: "192.0.2.1\n", want: "the tunnel address 192.0.2.1 is not on wg0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := systest.New()
			f.Up = !c.down
			e := apply.Env{P: sys.Paths{Config: t.TempDir(), Run: t.TempDir()}, R: f}
			st := c.state
			if st == nil && c.name != "no state" {
				st = &apply.State{Committed: 1, Applied: 1}
			}
			if st != nil {
				if err := e.WriteState(*st); err != nil {
					t.Fatal(err)
				}
			}
			if c.dns != "" {
				write(t, e.P.DNSState(), c.dns)
			}
			if c.address != "" {
				write(t, e.P.DNSAddress(), c.address)
			}
			err := Check(context.Background(), e)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestCheckAwgFails(t *testing.T) {
	f := systest.New()
	f.Up = true
	f.Fail["awg show interfaces"] = "no"
	e := apply.Env{P: sys.Paths{Config: t.TempDir(), Run: t.TempDir()}, R: f}
	if err := Check(context.Background(), e); err == nil || !strings.Contains(err.Error(), "is down") {
		t.Errorf("err = %v", err)
	}
}

func TestQueryNoAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Query(ctx, "127.0.0.2"); err == nil || !strings.Contains(err.Error(), "does not answer on 127.0.0.2") {
		t.Errorf("err = %v", err)
	}
}

// answer makes a DNS reply: 127.0.0.1 for an A question, no records else.
func answer(q []byte) []byte {
	if len(q) < 12 {
		return nil
	}
	i := 12
	for i < len(q) && q[i] != 0 {
		i += int(q[i]) + 1
	}
	if i+5 > len(q) {
		return nil
	}
	question := q[12 : i+5]
	isA := binary.BigEndian.Uint16(q[i+1:]) == 1
	r := append([]byte{}, q[:2]...)
	r = append(r, 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0)
	r = append(r, question...)
	if isA {
		r[7] = 1
		r = append(r, 0xc0, 12, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 127, 0, 0, 1)
	}
	return r
}

func TestQueryAnswer(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:53")
	if err != nil {
		t.Skipf("cannot listen on port 53: %v", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := answer(buf[:n]); r != nil {
				_, _ = pc.WriteTo(r, from)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := Query(ctx, "127.0.0.1"); err != nil {
		t.Error(err)
	}
}

func TestOnInterface(t *testing.T) {
	lo := net.ParseIP("127.0.0.1")
	if !onInterface(lo, "lo") {
		t.Skip("no loopback interface with 127.0.0.1")
	}
	if onInterface(net.ParseIP("192.0.2.1"), "lo") || onInterface(lo, "awgctl-none0") {
		t.Error("a wrong address or a missing interface must not match")
	}
}
