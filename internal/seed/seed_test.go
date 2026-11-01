package seed

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/config"
)

func env(m map[string]string) Getenv {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok && v != "" }
}

func rnd() *rand.Rand { return rand.New(rand.NewPCG(5, 6)) }

func noDetect(context.Context) (string, error) { return "", errors.New("offline") }

func TestBuildDefaults(t *testing.T) {
	st, _, err := Build(context.Background(), rnd(), env(map[string]string{"SERVERURL": "vpn.example.com"}), noDetect)
	if err != nil {
		t.Fatal(err)
	}
	if errs, _ := st.Validate(); len(errs) > 0 {
		t.Fatal(errs)
	}
	for k, want := range map[string]string{"net.subnet": "10.13.13.0/24", "server.endpoint_port": "51820",
		"net.mtu": "1280", "dns.upstream": "1.1.1.1,1.0.0.1", "fw.nat_interface": "eth+"} {
		if st.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, st.Get(k), want)
		}
	}
	if st.AWG["version"] != "3.1" || len(st.Peers) != 0 {
		t.Errorf("version %s, %d peers; want 3.1 and none", st.AWG["version"], len(st.Peers))
	}
}

func TestBuildFromEnv(t *testing.T) {
	st, _, err := Build(context.Background(), rnd(), env(map[string]string{
		"SERVERURL": "2001:db8::1", "SERVERPORT": "443", "INTERNAL_SUBNET": "10.8.8.0",
		"MTU": "1400", "AWG_VERSION": "2.0", "AWG_JC": "4", "AWG_RANDOM_TRAILERS": "on", "AWG_S2": "17",
		"DNS_UPSTREAM": "9.9.9.9",
	}), noDetect)
	if err != nil {
		t.Fatal(err)
	}
	if st.Get("server.endpoint_host") != "[2001:db8::1]" || st.Get("net.peer_mtu") != "1400" {
		t.Errorf("env not applied: %v", st.Settings)
	}
	p := st.AWG
	if p["version"] != "2.0" || p["jc"] != "4" || !p.On("random_trailers") {
		t.Errorf("AWG env not applied: %v", p)
	}
	// One pinned S value is used for all four when trailers are on.
	if p["s1"] != "17" || p["s4"] != "17" {
		t.Errorf("pinned S with trailers: %v", p)
	}
}

func TestBuildErrors(t *testing.T) {
	for name, e := range map[string]map[string]string{
		"auto":        {"SERVERURL": "auto"},
		"bad port":    {"SERVERURL": "a.example", "SERVERPORT": "08"},
		"bad subnet":  {"SERVERURL": "a.example", "INTERNAL_SUBNET": "10.0.0.0/16"},
		"bad version": {"SERVERURL": "a.example", "AWG_VERSION": "9"},
		"S below 12":  {"SERVERURL": "a.example", "AWG_VERSION": "3.0", "AWG_S1": "8"},
		"no endpoint": {},
	} {
		if _, _, err := Build(context.Background(), rnd(), env(e), noDetect); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBuildDetects(t *testing.T) {
	st, notes, err := Build(context.Background(), rnd(), env(nil),
		func(context.Context) (string, error) { return "203.0.113.7", nil })
	if err != nil || st.Get("server.endpoint_host") != "203.0.113.7" || len(notes) == 0 {
		t.Fatalf("%v %v %v", st, notes, err)
	}
}

func TestDrift(t *testing.T) {
	st, _, _ := Build(context.Background(), rnd(), env(map[string]string{"SERVERURL": "a.example"}), noDetect)
	got := Drift(st, env(map[string]string{"SERVERURL": "a.example", "SERVERPORT": "51820", "MTU": "1400", "PEERS": "3"}))
	joined := strings.Join(got, "\n")
	if len(got) != 2 || !strings.Contains(joined, "awgctl set net.mtu 1400") || !strings.Contains(joined, "PEERS") {
		t.Fatalf("drift = %v", got)
	}
}

func TestDetectIPv4PrefersListOrder(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(w, "198.51.100.1")
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "198.51.100.2")
	}))
	defer fast.Close()
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "<html>")
	}))
	defer junk.Close()
	ip, err := DetectIPv4(context.Background(), []string{junk.URL, slow.URL, fast.URL}, http.DefaultClient)
	if err != nil || ip != "198.51.100.1" {
		t.Fatalf("got %s %v; want the first valid answer in list order", ip, err)
	}
	if _, err := DetectIPv4(context.Background(), []string{junk.URL}, http.DefaultClient); err == nil {
		t.Fatal("junk accepted")
	}
}

func TestRetrying(t *testing.T) {
	old := RetryDelays
	RetryDelays = []time.Duration{0, time.Millisecond, time.Millisecond}
	defer func() { RetryDelays = old }()
	n := 0
	d := Retrying(func(context.Context) (string, error) {
		n++
		if n < 3 {
			return "", errors.New("down")
		}
		return "192.0.2.1", nil
	}, func(string) {})
	if ip, err := d(context.Background()); err != nil || ip != "192.0.2.1" || n != 3 {
		t.Fatalf("%s %v after %d tries", ip, err, n)
	}
}

var _ = config.Registry
