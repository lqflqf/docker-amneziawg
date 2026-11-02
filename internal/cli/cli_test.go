package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/lqflqf/docker-amneziawg/internal/apply"
	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/lock"
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
	h.run(ExitError, "peer", "add", "phone")     // duplicate name
	h.run(ExitUsage, "peer", "add", "bad name!") // invalid name
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

// sqlExec runs statements on the DB file directly, to make store calls fail.
func (h *harness) sqlExec(stmts ...string) {
	h.t.Helper()
	db, err := sql.Open("sqlite", h.app.P.DB())
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			h.t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitOK, "peer", "add", "phone")
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"init", "--bogus"},
		{"health", "--bogus"},
		{"status", "--bogus"},
		{"apply", "--bogus"},
		{"version", "--bogus"},
		{"get", "--bogus"},
		{"get", "a", "b"},
		{"get", "awg.jc"},
		{"get", "nope"},
		{"set", "--bogus"},
		{"set", "net.mtu"},
		{"reset", "--bogus"},
		{"reset"},
		{"reset", "awg.jc"},
		{"reset", "server.private_key"},
		{"reset", "server.endpoint_host"},
		{"server", "--bogus"},
		{"server"},
		{"server", "rotate"},
		{"awg"},
		{"awg", "show", "--bogus"},
		{"awg", "bogus"},
		{"awg", "version"},
		{"awg", "version", "9.9", "--yes"},
		{"awg", "set"},
		{"awg", "set", "jc"},
		{"awg", "set", "nope=1", "--yes"},
		{"peer"},
		{"peer", "bogus"},
		{"peer", "list", "--bogus"},
		{"peer", "add"},
		{"peer", "add", "x", "--octet", "1"},
		{"peer", "list", "x"},
		{"peer", "show"},
		{"peer", "export"},
		{"peer", "rename", "phone"},
		{"peer", "rename", "phone", "bad name!"},
		{"peer", "set"},
		{"peer", "set", "phone", "--keepalive", "maybe"},
		{"peer", "enable"},
		{"peer", "rotate"},
		{"peer", "rm"},
		{"import-legacy"},
		{"import-legacy", "--rename", "nope", "/x"},
	} {
		out := h.run(ExitUsage, args...)
		if !strings.HasPrefix(out, "awgctl: ") || !strings.Contains(out, "Usage: awgctl <command>") {
			t.Errorf("awgctl %v: no usage text:\n%s", args, out)
		}
	}
	if out := h.run(ExitOK, "help"); !strings.HasPrefix(out, "Usage: awgctl") {
		t.Fatalf("help:\n%s", out)
	}
	if p, _ := h.state().FindPeer("phone"); p.Name != "phone" {
		t.Fatal("a usage error changed the peer")
	}
}

func TestCommandsNeedSafeDatabase(t *testing.T) {
	h := newHarness(t)
	cmds := [][]string{{"status"}, {"get"}, {"apply"}, {"awg", "show"}, {"peer", "list"},
		{"peer", "show", "1"}, {"peer", "export", "1"}, {"set", "net.mtu", "1300"}}
	for _, c := range cmds {
		if out := h.run(ExitError, c...); !strings.Contains(out, "no database") {
			t.Errorf("%v before init:\n%s", c, out)
		}
	}
	h.run(ExitOK, "init")
	h.app.Owner = func() (fsafe.Owner, error) { return fsafe.Owner{}, errors.New("no such user") }
	for _, c := range append(cmds, []string{"init"}) {
		if out := h.run(ExitError, c...); !strings.Contains(out, "volume owner") {
			t.Errorf("%v with a bad owner:\n%s", c, out)
		}
	}
	h2 := newHarness(t)
	h2.run(ExitOK, "init")
	os.RemoveAll(h2.app.P.Backups())
	os.Symlink(t.TempDir(), h2.app.P.Backups())
	h2.run(ExitError, "status")
	h2.run(ExitError, "peer", "add", "phone")
	h2.run(ExitError, "init")
}

func TestLockContention(t *testing.T) {
	h := newHarness(t)
	h.run(ExitOK, "init")
	l, err := lock.Acquire(context.Background(), h.app.P.Lock())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	h.app.LockTTL = 50 * time.Millisecond
	h.run(ExitError, "peer", "add", "phone")
	h.run(ExitError, "apply")
	h.run(ExitError, "init")
	if len(h.state().Peers) != 0 {
		t.Fatal("a command changed the DB without the lock")
	}
}

func TestHealthVersionAndNew(t *testing.T) {
	h := newHarness(t)
	h.run(ExitOK, "init")
	if out := h.run(ExitError, "health"); !strings.Contains(out, "tunnel wg0 is down") {
		t.Fatalf("health:\n%s", out)
	}
	Version = "1.2.3-r4"
	defer func() { Version = "dev" }()
	out := h.run(ExitOK, "version")
	for _, want := range []string{"awgctl        1.2.3-r4", "awg           awg v0-test", "amneziawg-go  amneziawg-go v0-test", "schema        "} {
		if !strings.Contains(out, want) {
			t.Errorf("version: no %q:\n%s", want, out)
		}
	}
	h.fake.Fail["amneziawg-go"] = "not found"
	if out := h.run(ExitOK, "version"); !strings.Contains(out, "amneziawg-go  unknown") {
		t.Fatalf("version without amneziawg-go:\n%s", out)
	}

	a := New()
	if a.P != sys.DefaultPaths() || a.LockTTL <= 0 || a.Rand == nil || a.Owner == nil {
		t.Fatalf("New: %+v", a)
	}
	var buf bytes.Buffer
	a.Out = &buf
	if _, ok := a.Getenv("AWGCTL_TEST_UNSET_VAR"); ok {
		t.Fatal("Getenv found an unset var")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Detect(ctx); err == nil {
		t.Fatal("Detect with a canceled context did not fail")
	}
}

// dumpRunner gives fixed handshake and transfer counters for wg0 peers.
type dumpRunner struct {
	*systest.Fake
	dump string
}

func (r dumpRunner) Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, []byte, error) {
	if name == "awg" && len(args) == 3 && args[2] == "dump" {
		return []byte(r.dump), nil, nil
	}
	return r.Fake.Run(ctx, name, args, stdin, env)
}

func TestPeerListShowExport(t *testing.T) {
	h := newHarness(t)
	h.boot()
	if out := h.run(ExitOK, "peer", "list"); !strings.Contains(out, "No peers.") {
		t.Fatalf("empty list:\n%s", out)
	}
	if out := h.run(ExitOK, "peer", "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty json list:\n%s", out)
	}
	h.run(ExitOK, "peer", "add", "phone", "--octet", "9")
	h.run(ExitError, "peer", "add", "laptop", "--octet", "9")
	if out := h.run(ExitOK, "peer", "add", "--qr", "laptop"); !strings.Contains(out, "the QR code holds the peer's private key") {
		t.Fatalf("add --qr:\n%s", out)
	}
	h.run(ExitOK, "peer", "set", "laptop", "--keepalive", "off")
	if p, _ := h.state().FindPeer("laptop"); p.Keepalive {
		t.Fatal("keepalive is still on")
	}
	h.run(ExitError, "peer", "show", "nobody")
	h.run(ExitError, "peer", "export", "nobody")
	h.run(ExitError, "peer", "enable", "nobody")
	h.run(ExitError, "peer", "rm", "nobody", "--yes")

	phone, _ := h.state().FindPeer("phone")
	h.app.R = dumpRunner{Fake: h.fake, dump: "priv\tpub\t51820\toff\n" + phone.PublicKey +
		"\tpsk\t(none)\t10.13.13.9/32\t1700000000\t1536\t3221225472\toff\nshort line\n"}
	out := h.run(ExitOK, "peer", "list")
	if !regexp.MustCompile(`(?m)^1\s+phone\s+10\.13\.13\.9\s+true\s+true\s+2023-11-14T22:13:20Z\s+1\.5KiB/3\.0GiB$`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^2\s+laptop\s+10\.13\.13\.2\s+true\s+false\s+-\s+0B/0B$`).MatchString(out) {
		t.Fatalf("list:\n%s", out)
	}
	if out := h.run(ExitOK, "peer", "list", "--json"); !strings.Contains(out, `"last_handshake": "2023-11-14T22:13:20Z"`) ||
		!strings.Contains(out, `"rx_bytes": 1536`) {
		t.Fatalf("json list:\n%s", out)
	}
	out = h.run(ExitOK, "peer", "show", "phone")
	for _, want := range []string{"address     10.13.13.9/32", "preshared   true", "keepalive   true",
		"handshake   2023-11-14T22:13:20Z", "transfer    1.5KiB received, 3.0GiB sent"} {
		if !strings.Contains(out, want) {
			t.Errorf("show: no %q:\n%s", want, out)
		}
	}
	if out := h.run(ExitOK, "peer", "show", "laptop"); strings.Contains(out, "handshake") {
		t.Fatalf("show without a handshake:\n%s", out)
	}
	h.app.R = h.fake

	dir := t.TempDir()
	conf, png := filepath.Join(dir, "phone.conf"), filepath.Join(dir, "phone.png")
	out = h.run(ExitOK, "peer", "export", "phone", "--conf", conf, "--png", png)
	if strings.Contains(out, "[Interface]") || !strings.Contains(out, "Wrote "+conf) || !strings.Contains(out, "Wrote "+png) {
		t.Fatalf("export to files:\n%s", out)
	}
	if b, _ := os.ReadFile(conf); !strings.Contains(string(b), "Address = 10.13.13.9/32") {
		t.Fatalf("conf file:\n%s", b)
	}
	if b, _ := os.ReadFile(png); !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatal("the png file is not a PNG")
	}
	h.run(ExitError, "peer", "export", "phone", "--conf", filepath.Join(dir, "no", "x.conf"))
	h.run(ExitError, "peer", "export", "phone", "--png", filepath.Join(dir, "no", "x.png"))

	// A pending restart that changes the peer conf gives a warning.
	if out := h.run(ExitOK, "peer", "export", "phone"); strings.Contains(out, "WARNING") {
		t.Fatalf("export without a pending restart:\n%s", out)
	}
	h.run(ExitOK, "set", "net.subnet", "10.77.0.0/24", "--yes")
	h.run(ExitOK, "awg", "regenerate", "h1", "--yes")
	h.run(ExitOK, "awg", "set", "jc=7")
	out = h.run(ExitOK, "peer", "export", "phone")
	if !strings.Contains(out, "WARNING: a restart is pending for awg.h1, net.subnet;") || !strings.Contains(out, "Address = 10.77.0.9/32") {
		t.Fatalf("export with a pending restart:\n%s", out)
	}
}

func TestSettingsGetResetAndServerKey(t *testing.T) {
	h := newHarness(t)
	h.boot()
	key := h.state().Get("server.private_key")
	if out := h.run(ExitOK, "get", "server.private_key"); strings.TrimSpace(out) != masked {
		t.Fatalf("get secret:\n%s", out)
	}
	if out := h.run(ExitOK, "get", "server.private_key", "--show-secrets"); strings.TrimSpace(out) != key {
		t.Fatalf("get secret --show-secrets:\n%s", out)
	}
	if out := h.run(ExitOK, "get", "net.mtu"); out != "1280\n" {
		t.Fatalf("get net.mtu: %q", out)
	}
	out := h.run(ExitOK, "get", "--show-secrets")
	if !strings.Contains(out, key) || !regexp.MustCompile(`net\.mtu\s+1280  \(default\)`).MatchString(out) {
		t.Fatalf("get --show-secrets:\n%s", out)
	}
	if out := h.run(ExitOK, "set", "net.mtu", "1280"); !strings.Contains(out, "net.mtu is already 1280.") {
		t.Fatalf("set the same value:\n%s", out)
	}
	h.run(ExitOK, "set", "net.mtu", "1300")
	h.run(ExitOK, "reset", "net.mtu")
	if h.state().Get("net.mtu") != "1280" {
		t.Fatal("reset did not restore net.mtu")
	}
	// No peers: a new server key still needs --yes.
	if out := h.run(ExitNeedYes, "server", "rotate-key"); !strings.Contains(out, "the server (no peers yet)") {
		t.Fatalf("rotate-key:\n%s", out)
	}
	out = h.run(ExitOK, "server", "rotate-key", "--yes")
	if !strings.Contains(out, "New server public key: ") || !strings.Contains(out, "Pending restart: server.private_key") ||
		h.state().Get("server.private_key") == key {
		t.Fatalf("rotate-key --yes:\n%s", out)
	}
}

func TestAWGShowAndNoChange(t *testing.T) {
	h := newHarness(t)
	h.boot()
	hpk := h.state().AWG["header_protection_key"]
	if hpk == "" {
		t.Fatal("no header_protection_key under the default version")
	}
	if out := h.run(ExitOK, "awg", "show"); strings.Contains(out, hpk) || !strings.Contains(out, masked) {
		t.Fatalf("show:\n%s", out)
	}
	if out := h.run(ExitOK, "awg", "show", "--show-secrets"); !strings.Contains(out, hpk) {
		t.Fatalf("show --show-secrets:\n%s", out)
	}
	jc := h.state().AWG["jc"]
	if out := h.run(ExitOK, "awg", "set", "jc="+jc); !strings.Contains(out, "No change.") {
		t.Fatalf("set the same value:\n%s", out)
	}
	out := h.run(ExitOK, "awg", "regenerate", "header_protection_key", "--yes")
	if !strings.Contains(out, "awg.header_protection_key: (new secret)") {
		t.Fatalf("regenerate:\n%s", out)
	}
	if out := h.run(ExitOK, "awg", "set", "s=30", "--yes"); !strings.Contains(out, "WARNING: ") {
		t.Fatalf("set s=30 gives no warning:\n%s", out)
	}
	if out := h.run(ExitOK, "awg", "version", "1.5", "--yes"); !strings.Contains(out, "awg.i1: (removed)") {
		t.Fatalf("version 1.5:\n%s", out)
	}
	h.run(ExitOK, "init")
}

func TestStatusShowsDatapathAndApplyError(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.env["WG_QUICK_USERSPACE_IMPLEMENTATION"] = "amneziawg-go"
	h.run(ExitOK, "peer", "add", "phone")
	h.run(ExitOK, "peer", "disable", "phone")
	h.fake.Fail["awg syncconf"] = "Unable to modify interface"
	h.run(ExitNotApplied, "peer", "add", "laptop")
	out := h.run(ExitOK, "status")
	for _, want := range []string{"tunnel      wg0 up (userspace (amneziawg-go))", "endpoint    vpn.example.com:51820",
		"subnet      10.13.13.0/24", "awg         3.1 (RandomTrailers on, DisableCookies", "peers       2 (1 enabled)",
		"apply       generation 4 saved, 3 applied: "} {
		if !strings.Contains(out, want) {
			t.Errorf("status: no %q:\n%s", want, out)
		}
	}
}

func TestStoreFailuresChangeNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  string
		args []string
	}{
		{"settings", "CREATE TRIGGER f BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"set", "net.mtu", "1300"}},
		{"insert peer", "CREATE TRIGGER f BEFORE INSERT ON peers BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"peer", "add", "tablet"}},
		{"update peer", "CREATE TRIGGER f BEFORE UPDATE ON peers BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"peer", "disable", "phone"}},
		{"delete peer", "CREATE TRIGGER f BEFORE DELETE ON peers BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"peer", "rm", "phone", "--yes"}},
		{"load", "ALTER TABLE peers RENAME TO gone", []string{"peer", "add", "tablet"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.boot()
			h.run(ExitOK, "peer", "add", "phone")
			gen := h.state().Generation
			h.sqlExec(c.sql)
			if out := h.run(ExitError, c.args...); !strings.Contains(out, "awgctl: ") {
				t.Fatalf("output:\n%s", out)
			}
			if c.name == "load" {
				h.run(ExitError, "init")
				h.run(ExitError, "status")
				return
			}
			if st := h.state(); st.Generation != gen || len(st.Peers) != 1 || !st.Peers[0].Enabled || st.Get("net.mtu") != "1280" {
				t.Fatalf("the DB changed: %+v", st)
			}
		})
	}
}

func TestApplyStateWriteFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.boot()
	os.Remove(h.app.P.StateJSON())
	os.Mkdir(h.app.P.StateJSON(), 0o700)
	out := h.run(ExitOK, "peer", "add", "phone")
	if !strings.Contains(out, "could not write the apply state") || !strings.Contains(out, "Applied.") {
		t.Fatalf("output:\n%s", out)
	}
	os.Remove(h.app.P.ActiveJSON())
	if out := h.run(ExitNotApplied, "apply"); !strings.Contains(out, "saved, not applied") {
		t.Fatalf("apply without active.json:\n%s", out)
	}
}

func TestInitFailures(t *testing.T) {
	h := newHarness(t)
	h.run(ExitOK, "init")
	h.sqlExec("PRAGMA user_version = 99")
	if out := h.run(ExitError, "init"); !strings.Contains(out, "restore a backup made by this one") {
		t.Fatalf("newer schema:\n%s", out)
	}
	h.sqlExec("PRAGMA user_version = 1")

	h.fake.Fail["awg-quick strip"] = "parse error"
	if out := h.run(ExitError, "init"); !strings.Contains(out, "config check failed") {
		t.Fatalf("check:\n%s", out)
	}
	delete(h.fake.Fail, "awg-quick strip")

	os.Remove(h.app.P.DNSAddress())
	os.Mkdir(h.app.P.DNSAddress(), 0o700)
	os.WriteFile(filepath.Join(h.app.P.DNSAddress(), "x"), nil, 0o600)
	h.run(ExitError, "init")
	os.RemoveAll(h.app.P.DNSAddress())

	// A run dir that cannot be made is fatal.
	run := h.app.P.Run
	os.RemoveAll(h.app.P.DNSDir())
	os.WriteFile(h.app.P.DNSDir(), nil, 0o600)
	h.run(ExitError, "init")
	h.app.P.Run = filepath.Join(run, "dnsmasq")
	h.run(ExitError, "init")
	h.app.P.Run = run
	os.Remove(h.app.P.DNSDir())

	// Warnings do not stop the start; the userspace datapath skips the module check.
	h.env["WG_QUICK_USERSPACE_IMPLEMENTATION"] = "amneziawg-go"
	h.sqlExec("UPDATE settings SET value = '30' WHERE key IN ('awg.s1', 'awg.s2', 'awg.s3', 'awg.s4')")
	if out := h.run(ExitOK, "init"); !strings.Contains(out, "**** WARNING: ") || !strings.Contains(out, "Config initialization finished") {
		t.Fatalf("init with warnings:\n%s", out)
	}
}

func TestSeedFailures(t *testing.T) {
	h := newHarness(t)
	h.fake.Fail["awg-quick strip"] = "parse error"
	if out := h.run(ExitError, "init"); !strings.Contains(out, "the seeded config is not valid") {
		t.Fatalf("seed check:\n%s", out)
	}
	if _, err := os.Stat(h.app.P.DB()); err == nil {
		t.Fatal("a DB was created from a config that is not valid")
	}
	h2 := newHarness(t)
	h2.env = map[string]string{}
	if out := h2.run(ExitOK, "init"); !strings.Contains(out, "203.0.113.9") {
		t.Fatalf("seed without SERVERURL:\n%s", out)
	}
}

// writeLegacy writes a volume of the shell release into dir: one conf per
// peer id, keys in files and the saved state as ORIG_ lines.
func writeLegacy(t *testing.T, dir, peers, extraUp string, ids ...string) {
	t.Helper()
	w := func(rel, body string) {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	params := awg.Generate(rand.New(rand.NewPCG(1, 1)), "2.0")
	var awgLines, paramFile strings.Builder
	for _, l := range awg.Lines(params) {
		fmt.Fprintf(&awgLines, "%s = %s\n", l.Key, l.Value)
	}
	for _, k := range awg.AllKeys {
		fmt.Fprintf(&paramFile, "%s=%s\n", awg.EnvName(k), params[k])
	}
	sp, spub := keys.Pair()
	w("server/privatekey-server", sp+"\n")
	w("server/publickey-server", spub+"\n")
	w("server/awg_params", paramFile.String())
	w(".donoteditthisfile", fmt.Sprintf("ORIG_SERVERURL=\"vpn.example.com\"\nORIG_SERVERPORT=\"51820\"\nORIG_PEERS=\"%s\"\n"+
		"ORIG_INTERFACE=\"10.20.30\"\nORIG_ALLOWEDIPS=\"0.0.0.0/0, ::/0\"\n", peers))
	srv := "[Interface]\nAddress = 10.20.30.1\nListenPort = 51820\nPrivateKey = " + sp + "\nMTU = 1280\n" +
		"PostUp = iptables -A FORWARD -i %i -j ACCEPT\nPostUp = iptables -A FORWARD -o %i -j ACCEPT\n" +
		"PostUp = iptables -t nat -A POSTROUTING -o eth+ -j MASQUERADE\n" + extraUp +
		"PostDown = iptables -t nat -D POSTROUTING -o eth+ -j MASQUERADE || true\n" +
		"PostDown = iptables -D FORWARD -o %i -j ACCEPT || true\nPostDown = iptables -D FORWARD -i %i -j ACCEPT || true\n" +
		awgLines.String() + "\n"
	for i, id := range ids {
		pp, ppub := keys.Pair()
		psk := keys.NewSymmetric()
		ip := fmt.Sprintf("10.20.30.%d", i+2)
		w(id+"/privatekey-"+id, pp+"\n")
		w(id+"/publickey-"+id, ppub+"\n")
		w(id+"/presharedkey-"+id, psk+"\n")
		w(id+"/"+id+".conf", "[Interface]\nAddress = "+ip+"\nPrivateKey = "+pp+"\nMTU = 1280\nDNS = 10.20.30.1\n"+
			awgLines.String()+"\n[Peer]\nPublicKey = "+spub+"\nPresharedKey = "+psk+
			"\nEndpoint = vpn.example.com:51820\nAllowedIPs = 0.0.0.0/0, ::/0\n")
		srv += "[Peer]\n# " + id + "\nPublicKey = " + ppub + "\nPresharedKey = " + psk +
			"\nAllowedIPs = " + ip + "/32\nPersistentKeepalive = 25\n\n"
	}
	w("wg_confs/wg0.conf", srv)
	w("templates/dnsmasq.conf", "domain-needed\nbogus-priv\nno-hosts\ncache-size=1000\n${DNS_SERVER_LINES}\n")
}

func TestImportLegacyInPlace(t *testing.T) {
	h := newHarness(t)
	cfg := h.app.P.Config
	writeLegacy(t, cfg, "1,peer1", "", "peer1", "peer_peer1")
	h.run(ExitError, "init")

	out := h.run(ExitError, "import-legacy", cfg)
	if !strings.Contains(out, "BLOCKER:  ") || !strings.Contains(out, "nothing was written") {
		t.Fatalf("name collision:\n%s", out)
	}
	out = h.run(ExitOK, "import-legacy", cfg, "--rename", "peer_peer1=peer1b", "--dry-run")
	for _, want := range []string{"server:   endpoint vpn.example.com:51820, subnet 10.20.30.0/24, AWG 2.0, 2 peer(s)",
		"result:   every peer conf is unchanged", "dry run:  nothing was written"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run: no %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(h.app.P.DB()); err == nil {
		t.Fatal("a dry run wrote the DB")
	}

	h.fake.Fail["awg-quick strip"] = "parse error"
	if out := h.run(ExitError, "import-legacy", cfg, "--rename", "peer_peer1=peer1b"); !strings.Contains(out, "does not pass the checks") {
		t.Fatalf("check:\n%s", out)
	}
	delete(h.fake.Fail, "awg-quick strip")

	out = h.run(ExitOK, "import-legacy", cfg, "--rename", "peer_peer1=peer1b")
	if !strings.Contains(out, "imported: "+h.app.P.DB()) || !strings.Contains(out, "moved:    the old files to ") {
		t.Fatalf("import:\n%s", out)
	}
	entries, _ := os.ReadDir(cfg)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "amneziawg.db" || !strings.HasPrefix(names[1], "legacy-") {
		t.Fatalf("/config after the import: %v", names)
	}
	if _, err := os.Stat(filepath.Join(cfg, names[1], "wg_confs", "wg0.conf")); err != nil {
		t.Fatalf("the old files were not moved: %v", err)
	}
	st := h.state()
	if len(st.Peers) != 2 || st.Peers[1].Name != "peer1b" || st.Subnet() != "10.20.30.0/24" {
		t.Fatalf("imported state: %+v", st.Peers)
	}
	if out := h.run(ExitError, "import-legacy", cfg); !strings.Contains(out, "runs only once") {
		t.Fatalf("second import:\n%s", out)
	}
	h.run(ExitOK, "init")
}

func TestImportLegacyPartial(t *testing.T) {
	h := newHarness(t)
	old := t.TempDir()
	writeLegacy(t, old, "2", "PostUp = iptables -A FORWARD -p tcp --dport 22 -j DROP\n", "peer1", "peer2")
	os.WriteFile(filepath.Join(old, "peer2", "peer2.conf"), []byte("[Interface]\nPrivateKey = x\n"), 0o600)
	os.Remove(filepath.Join(old, "server", "awg_params"))
	if out := h.run(ExitError, "import-legacy", filepath.Join(old, "missing")); !strings.Contains(out, "missing") {
		t.Fatalf("missing dir:\n%s", out)
	}
	out := h.run(ExitError, "import-legacy", old)
	if !strings.Contains(out, "note:     ") || !strings.Contains(out, "drop:     custom PostUp") || !strings.Contains(out, "--force-partial") {
		t.Fatalf("drop:\n%s", out)
	}
	if !regexp.MustCompile(`peer:     peer2\s+changed \(.+\) -> export a new conf for this peer`).MatchString(out) ||
		strings.Contains(out, "every peer conf is unchanged") {
		t.Fatalf("verdicts:\n%s", out)
	}
	if _, err := os.Stat(h.app.P.DB()); err == nil {
		t.Fatal("a refused import wrote the DB")
	}
	out = h.run(ExitOK, "import-legacy", old, "--force-partial")
	if strings.Contains(out, "moved:") || !strings.Contains(out, "next:     start the container") {
		t.Fatalf("import from another dir:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(old, "wg_confs")); err != nil {
		t.Fatal("an import from another dir moved the old files")
	}
	if len(h.state().Peers) != 2 {
		t.Fatalf("peers %+v", h.state().Peers)
	}
}
