package cli

import (
	"bytes"
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/apply"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
	"github.com/lqflqf/docker-amneziawg/internal/sys/systest"
)

type harness struct {
	t    *testing.T
	app  *App
	fake *systest.Fake
	out  *bytes.Buffer
	env  map[string]string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, fake: systest.New(), out: &bytes.Buffer{},
		env: map[string]string{"SERVERURL": "vpn.example.com"}}
	h.app = &App{
		P:      sys.Paths{Config: t.TempDir(), Run: t.TempDir()},
		R:      h.fake,
		Out:    h.out,
		Err:    h.out,
		Getenv: func(k string) (string, bool) { v, ok := h.env[k]; return v, ok && v != "" },
		Detect: func(context.Context) (string, error) { return "203.0.113.9", nil },
		Rand:   rand.New(rand.NewPCG(7, 8)),
		Owner:  func() (fsafe.Owner, error) { return fsafe.Owner{UID: os.Getuid(), GID: os.Getgid()}, nil },
	}
	h.app.LockTTL = 5e9
	os.MkdirAll(h.app.P.S6DNS(), 0o755)
	return h
}

// run runs awgctl and checks the exit code.
func (h *harness) run(want int, args ...string) string {
	h.t.Helper()
	h.out.Reset()
	if got := h.app.Main(args); got != want {
		h.t.Fatalf("awgctl %s: exit %d, want %d\n%s", strings.Join(args, " "), got, want, h.out.String())
	}
	return h.out.String()
}

// boot runs init and marks wg0 as up, the way svc-amneziawg would.
func (h *harness) boot() {
	h.t.Helper()
	h.run(ExitOK, "init")
	h.fake.Up = true
	os.WriteFile(h.app.P.Settled(), nil, 0o644)
	h.run(ExitOK, "apply")
}

func (h *harness) state() *config.State {
	h.t.Helper()
	st, err := h.app.view(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) applyState() apply.State {
	s, _ := apply.Env{P: h.app.P}.ReadState()
	return s
}

func TestInitSeedsAndRendersRuntime(t *testing.T) {
	h := newHarness(t)
	out := h.run(ExitOK, "init")
	if !strings.Contains(out, "Config initialization finished") || !strings.Contains(out, "No peers yet") {
		t.Fatalf("init output:\n%s", out)
	}
	for _, p := range []string{h.app.P.DB(), h.app.P.WG0(), h.app.P.ActiveJSON(), h.app.P.StateJSON(), h.app.P.DNSConf(), h.app.P.DNSAddress()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s", p)
		}
	}
	if fi, _ := os.Stat(h.app.P.DB()); fi.Mode().Perm() != 0o600 {
		t.Errorf("DB mode %v", fi.Mode())
	}
	if _, err := os.Stat(h.app.P.DB() + "-wal"); err == nil {
		t.Error("a -wal file exists: one-file backups would miss data")
	}
	b, _ := os.ReadFile(h.app.P.DNSAddress())
	if strings.TrimSpace(string(b)) != "10.13.13.1" {
		t.Errorf("dns address %q", b)
	}
	// A second start does not change the DB, and warns about env drift.
	gen := h.state().Generation
	h.env["SERVERPORT"] = "443"
	out = h.run(ExitOK, "init")
	if !strings.Contains(out, "SERVERPORT=443 differs") || h.state().Generation != gen {
		t.Fatalf("second start:\n%s", out)
	}
}

func TestInitRefusesLegacyVolumeAndBadDB(t *testing.T) {
	h := newHarness(t)
	os.MkdirAll(filepath.Join(h.app.P.Config, "wg_confs"), 0o700)
	if out := h.run(ExitError, "init"); !strings.Contains(out, "import-legacy") {
		t.Fatalf("legacy volume:\n%s", out)
	}
	if _, err := os.Stat(h.app.P.DB()); err == nil {
		t.Fatal("a DB was created over a legacy volume")
	}

	h2 := newHarness(t)
	h2.env = map[string]string{}
	h2.app.Detect = func(context.Context) (string, error) { return "", os.ErrDeadlineExceeded }
	h2.run(ExitError, "init")
	entries, _ := os.ReadDir(h2.app.P.Config)
	if len(entries) != 0 {
		t.Fatalf("a failed seed left %v", entries)
	}
}

func TestPeerLifecycleLive(t *testing.T) {
	h := newHarness(t)
	h.boot()
	out := h.run(ExitOK, "peer", "add", "phone")
	if !strings.Contains(out, "Peer #1 phone added") || !strings.Contains(out, "Applied.") {
		t.Fatalf("add:\n%s", out)
	}
	h.run(ExitOK, "peer", "add", "laptop", "--no-keepalive")
	if len(h.fake.Peers) != 2 || !h.fake.Called("awg syncconf wg0") {
		t.Fatalf("wg0 peers %v", h.fake.Peers)
	}
	h.run(ExitError, "peer", "add", "phone")      // duplicate name
	h.run(ExitUsage, "peer", "add", "bad name!")  // invalid name
	h.run(ExitOK, "peer", "disable", "phone")
	if len(h.fake.Peers) != 1 {
		t.Fatalf("disabled peer still in wg0: %v", h.fake.Peers)
	}
	h.run(ExitOK, "peer", "enable", "1")
	h.run(ExitOK, "peer", "rename", "#1", "phone2")
	h.run(ExitNeedYes, "peer", "rm", "phone2")
	h.run(ExitNeedYes, "peer", "rotate", "laptop")
	h.run(ExitOK, "peer", "rotate", "laptop", "--yes")
	h.run(ExitOK, "peer", "rm", "phone2", "--yes")
	st := h.state()
	if len(st.Peers) != 1 || st.Peers[0].Name != "laptop" {
		t.Fatalf("peers %+v", st.Peers)
	}
	// The id of a deleted peer is not used again.
	h.run(ExitOK, "peer", "add", "tablet")
	if p, _ := h.state().FindPeer("tablet"); p.ID != 3 {
		t.Fatalf("tablet got id %d, want 3", p.ID)
	}
	if out := h.run(ExitOK, "peer", "export", "tablet"); !strings.Contains(out, "Endpoint = vpn.example.com:51820") {
		t.Fatalf("export:\n%s", out)
	}
	if out := h.run(ExitOK, "peer", "list", "--json"); !strings.Contains(out, `"name": "tablet"`) {
		t.Fatalf("list:\n%s", out)
	}
	if s := h.applyState(); s.Pending() {
		t.Fatalf("apply state pending: %+v", s)
	}
}

func TestPendingRestartNeverReachesLiveInterface(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitOK, "peer", "add", "phone")
	before, _ := os.ReadFile(h.app.P.WG0())
	h.run(ExitNeedYes, "set", "net.subnet", "10.77.0.0/24")
	out := h.run(ExitOK, "set", "net.subnet", "10.77.0.0/24", "--yes")
	if !strings.Contains(out, "Pending restart: net.subnet") {
		t.Fatalf("set:\n%s", out)
	}
	h.run(ExitOK, "awg", "set", "jc=99", "--yes")
	h.run(ExitOK, "peer", "add", "laptop")
	after, _ := os.ReadFile(h.app.P.WG0())
	iface := func(b []byte) string { s := string(b); return s[:strings.Index(s, "[Peer]")] }
	if iface(before) != iface(after) {
		t.Fatalf("a peer command changed [Interface]:\n%s\n---\n%s", iface(before), iface(after))
	}
	if !strings.Contains(string(after), "AllowedIPs = 10.13.13.3/32") {
		t.Fatalf("the new peer must use the active subnet:\n%s", after)
	}
	if out := h.run(ExitOK, "status"); !strings.Contains(out, "pending     restart for: awg.jc, net.subnet") {
		t.Fatalf("status:\n%s", out)
	}
	// After the restart the pending values are active.
	h.boot()
	b, _ := os.ReadFile(h.app.P.WG0())
	if !strings.Contains(string(b), "Address = 10.77.0.1/24") || !strings.Contains(string(b), "Jc = 99") {
		t.Fatalf("restart did not apply:\n%s", b)
	}
}

func TestApplyFailureIsVisible(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitOK, "peer", "add", "phone")
	h.fake.Fail["awg syncconf"] = "Unable to modify interface"
	out := h.run(ExitNotApplied, "peer", "add", "laptop")
	if !strings.Contains(out, "saved, not applied") {
		t.Fatalf("output:\n%s", out)
	}
	if s := h.applyState(); !s.Pending() || s.ApplyError == "" {
		t.Fatalf("state %+v", s)
	}
	if len(h.state().Peers) != 2 {
		t.Fatal("the saved peer is missing from the DB")
	}
	delete(h.fake.Fail, "awg syncconf")
	h.run(ExitOK, "apply")
	if s := h.applyState(); s.Pending() || len(h.fake.Peers) != 2 {
		t.Fatalf("after apply: %+v %v", s, h.fake.Peers)
	}
	// A removed peer that stays in wg0 is a failure, not a success.
	h.fake.Ghost = h.fake.Peers[0]
	h.run(ExitNotApplied, "peer", "rm", "phone", "--yes")
}

func TestChangesWhileDownAreAppliedLater(t *testing.T) {
	h := newHarness(t)
	h.run(ExitOK, "init")
	out := h.run(ExitOK, "peer", "add", "phone")
	if !strings.Contains(out, "applied when the tunnel starts") || h.fake.Called("awg syncconf") {
		t.Fatalf("down:\n%s", out)
	}
	if !h.applyState().Pending() {
		t.Fatal("not pending while down")
	}
	h.fake.Up = true
	os.WriteFile(h.app.P.Settled(), nil, 0o644)
	h.run(ExitOK, "apply")
	if h.applyState().Pending() || len(h.fake.Peers) != 1 {
		t.Fatal("apply after up did not apply")
	}
}

func TestSettingsCommands(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitUsage, "set", "awg.s1", "20")
	h.run(ExitUsage, "set", "server.private_key", "x")
	h.run(ExitUsage, "set", "nope", "1")
	h.run(ExitError, "set", "fw.nat_interface", "eth0;reboot")
	h.run(ExitOK, "set", "dns.upstream", "9.9.9.9, 149.112.112.112")
	b, _ := os.ReadFile(h.app.P.DNSConf())
	if !strings.Contains(string(b), "server=9.9.9.9\nserver=149.112.112.112\n") || !h.fake.Called("s6-svc -r") {
		t.Fatalf("dns not applied:\n%s", b)
	}
	if out := h.run(ExitOK, "get"); !regexp.MustCompile(`dns\.upstream\s+9\.9\.9\.9,149\.112\.112\.112\n`).MatchString(out) ||
		!strings.Contains(out, "******** (secret") {
		t.Fatalf("get:\n%s", out)
	}
	h.run(ExitOK, "reset", "dns.upstream")
	if h.state().Get("dns.upstream") != "1.1.1.1,1.0.0.1" {
		t.Fatal("reset did not restore the default")
	}
	// No peers: an endpoint change needs no --yes.
	h.run(ExitOK, "set", "server.endpoint_host", "vpn2.example.com")
	h.run(ExitOK, "peer", "add", "phone")
	h.run(ExitNeedYes, "set", "server.endpoint_host", "vpn3.example.com")
	h.run(ExitNeedYes, "server", "rotate-key")
}

func TestAWGCommands(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitOK, "peer", "add", "phone")
	h.run(ExitNeedYes, "awg", "version", "2.0")
	h.run(ExitOK, "awg", "version", "2.0", "--yes")
	st := h.state()
	if st.AWG["version"] != "2.0" || st.AWG.On("random_trailers") || st.AWG["header_protection_key"] != "" {
		t.Fatalf("2.0: %v", st.AWG)
	}
	h.run(ExitError, "awg", "set", "random_trailers=on", "--yes") // S values unequal
	h.run(ExitOK, "awg", "set", "random_trailers=on", "s=16", "--yes")
	h.run(ExitOK, "awg", "set", "jc=5") // must not match: no --yes needed
	h.run(ExitUsage, "awg", "set", "version=3.0")
	if out := h.run(ExitOK, "awg", "show"); !strings.Contains(out, "RandomTrailers = on") || !strings.Contains(out, "S4 = 16") {
		t.Fatalf("show:\n%s", out)
	}
	h.run(ExitOK, "awg", "regenerate", "h1", "--yes")
}

func TestHandEditedDBFailsInit(t *testing.T) {
	h := newHarness(t)
	h.run(ExitOK, "init")
	db, err := store.Open(h.app.P.DB(), h.app.P.Backups())
	if err != nil {
		t.Fatal(err)
	}
	db.Update(context.Background(), func(tx *store.Tx) error {
		return tx.SetSetting("fw.nat_interface", "eth0; touch /pwned")
	})
	db.Close()
	if out := h.run(ExitError, "init"); !strings.Contains(out, "fw.nat_interface") {
		t.Fatalf("init:\n%s", out)
	}
}
