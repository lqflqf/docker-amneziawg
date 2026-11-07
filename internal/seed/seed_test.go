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

func TestBuildAWGPinErrors(t *testing.T) {
	for name, c := range map[string]struct {
		env  map[string]string
		want string
	}{
		"bad switch": {map[string]string{"AWG_RANDOM_TRAILERS": "maybe"}, `AWG_RANDOM_TRAILERS must be on or off, got "maybe"`},
		"3.x key":    {map[string]string{"AWG_VERSION": "2.0", "AWG_HEADER_PROTECTION_KEY": "x"}, "exists only in AWG 3.0 and 3.1"},
		"bad value":  {map[string]string{"AWG_JC": "500"}, "the AWG_* values are not valid: Jc (500) must be 0-128"},
	} {
		c.env["SERVERURL"] = "a.example"
		_, _, err := Build(context.Background(), rnd(), env(c.env), noDetect)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestDetectIPv4Failures(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "198.51.100.9\n")
	}))
	defer ok.Close()
	// The default client dials tcp4, which reaches the local test server.
	if ip, err := DetectIPv4(context.Background(), []string{ok.URL}, nil); err != nil || ip != "198.51.100.9" {
		t.Fatalf("default client: %s %v", ip, err)
	}
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "198.51.100.10", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	ip, err := DetectIPv4(context.Background(), []string{"://bad", closed.URL, failing.URL}, http.DefaultClient)
	if err == nil || ip != "" || !strings.Contains(err.Error(), "no service answered") {
		t.Fatalf("got %q %v", ip, err)
	}
}

func TestRetryingStops(t *testing.T) {
	old := RetryDelays
	defer func() { RetryDelays = old }()
	down := func(context.Context) (string, error) { return "", errors.New("down") }

	RetryDelays = []time.Duration{0, time.Millisecond}
	var logs []string
	_, err := Retrying(down, func(s string) { logs = append(logs, s) })(context.Background())
	if err == nil || err.Error() != "down" {
		t.Fatalf("all attempts failed: got %v, want the last error", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "(down), trying again in 1ms (attempt 2 of 2)") {
		t.Fatalf("logs = %q", logs)
	}

	RetryDelays = []time.Duration{0, time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = Retrying(down, func(string) { cancel() })(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait: got %v", err)
	}
}
