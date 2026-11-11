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

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
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
		P:      sys.Paths{Config: t.TempDir(), Run: t.TempDir(), Sys: t.TempDir()},
		R:      h.fake,
		Out:    h.out,
		Err:    h.out,
		Getenv: func(k string) (string, bool) { v, ok := h.env[k]; return v, ok && v != "" },
		Detect: func(context.Context) (string, error) { return "203.0.113.9", nil },
		Rand:   rand.New(rand.NewPCG(7, 8)),
		Owner:  func() (fsafe.Owner, error) { return fsafe.Owner{UID: os.Getuid(), GID: os.Getgid()}, nil },
	}
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

// boot runs init and marks wg0 as up, the way a container start would.
func (h *harness) boot() {
	h.t.Helper()
	h.run(ExitOK, "init")
	h.fake.Up = true
}

func (h *harness) state() *config.State {
	h.t.Helper()
	st, err := h.app.view(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *harness) peer(id int) store.Peer {
	h.t.Helper()
	p, err := h.state().FindPeer(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return *p
}

func (h *harness) wg0() string {
	h.t.Helper()
	b, err := os.ReadFile(h.app.P.WG0())
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func TestInitSeedsAndRendersRuntime(t *testing.T) {
	h := newHarness(t)
	out := h.run(ExitOK, "init")
	if !strings.Contains(out, "Config initialization finished") || !strings.Contains(out, "No peers yet") {
		t.Fatalf("init output:\n%s", out)
	}
	for _, p := range []string{h.app.P.DB(), h.app.P.WG0(), h.app.P.DNSConf(), h.app.P.DNSAddress()} {
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
	// A second start does not read the env again.
	h.env["SERVERPORT"] = "443"
	out = h.run(ExitOK, "init")
	if strings.Contains(out, "SERVERPORT") || h.state().Get("server.endpoint_port") != "51820" {
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

func TestPeerLifecycle(t *testing.T) {
	h := newHarness(t)
	h.boot()
	before := h.wg0()
	out := h.run(ExitOK, "peer", "add", "--name", "phone")
	for _, want := range []string{`Peer 2 "phone" added: address 10.13.13.2`, "Saved. Restart the container to apply.",
		"Export: awgctl peer export 2 --qr"} {
		if !strings.Contains(out, want) {
			t.Errorf("add: no %q:\n%s", want, out)
		}
	}
	if out := h.run(ExitOK, "peer", "add", "--no-keepalive"); !strings.Contains(out, "Peer 3 added: address 10.13.13.3") {
		t.Fatalf("add without a name:\n%s", out)
	}
	if p := h.peer(3); p.Name != "peer 3" || p.Keepalive {
		t.Fatalf("default peer %+v", p)
	}
	h.run(ExitError, "peer", "add", "--name", "phone")     // duplicate name
	h.run(ExitUsage, "peer", "add", "--name", "bad name!") // invalid name
	h.run(ExitError, "peer", "add", "--name", "peer 9")    // the default name of another id
	h.run(ExitOK, "peer", "disable", "2")
	if h.peer(2).Enabled {
		t.Fatal("peer 2 is still enabled")
	}
	h.run(ExitOK, "peer", "enable", "2")
	h.run(ExitOK, "peer", "rename", "2", "my phone")
	h.run(ExitOK, "peer", "rename", "3", "laptop")
	h.run(ExitOK, "peer", "rename", "3", "--clear")
	if a, b := h.peer(2).Name, h.peer(3).Name; a != "my phone" || b != "peer 3" {
		t.Fatalf("names %q %q", a, b)
	}
	h.run(ExitNeedYes, "peer", "rm", "2")
	h.run(ExitNeedYes, "peer", "rotate", "3")
	key := h.peer(3).PublicKey
	if out := h.run(ExitOK, "peer", "rotate", "3", "--yes"); !strings.Contains(out, "New conf needed for peer 3 after the restart") {
		t.Fatalf("rotate:\n%s", out)
	}
	if h.peer(3).PublicKey == key {
		t.Fatal("rotate kept the key")
	}
	h.run(ExitOK, "peer", "rm", "2", "--yes")
	st := h.state()
	if len(st.Peers) != 1 || st.Peers[0].ID != 3 {
		t.Fatalf("peers %+v", st.Peers)
	}
	// The id is the address: a new peer takes the lowest free one.
	h.run(ExitOK, "peer", "add", "--name", "tablet")
	if p := h.peer(2); p.Name != "tablet" {
		t.Fatalf("peer 2 is %+v, want tablet", p)
	}
	if out := h.run(ExitOK, "peer", "export", "2"); !strings.Contains(out, "Endpoint = vpn.example.com:51820") {
		t.Fatalf("export:\n%s", out)
	}
	if out := h.run(ExitOK, "peer", "list", "--json"); !strings.Contains(out, `"name": "tablet"`) || strings.Contains(out, "created_at") {
		t.Fatalf("list:\n%s", out)
	}
	// Nothing reaches the running system before the restart.
	if h.wg0() != before || h.fake.Called("awg syncconf") || h.fake.Called("s6-svc") {
		t.Fatalf("a peer command changed the running system:\n%s", h.wg0())
	}
	h.boot()
	if w := h.wg0(); !strings.Contains(w, "# #2 tablet") || !strings.Contains(w, "# #3 peer 3") ||
		!strings.Contains(w, "AllowedIPs = 10.13.13.2/32") {
		t.Fatalf("restart did not apply the peers:\n%s", w)
	}
}

func TestPeerAddMany(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitOK, "peer", "add", "--name", "phone")
	out := h.run(ExitOK, "peer", "add-many", "3", "--no-keepalive")
	for _, want := range []string{"Peer 3 added: address 10.13.13.3", "Peer 5 added: address 10.13.13.5",
		"3 peer(s) added. Export each one: awgctl peer export <id> --qr", "Saved. Restart the container to apply."} {
		if !strings.Contains(out, want) {
			t.Errorf("add-many: no %q:\n%s", want, out)
		}
	}
	h.run(ExitOK, "peer", "rm", "4", "--yes")
	h.run(ExitOK, "peer", "add-many", "2")
	var ids []int
	for _, p := range h.state().Peers {
		ids = append(ids, p.ID)
		if p.ID != 2 && p.Name != fmt.Sprintf("peer %d", p.ID) {
			t.Errorf("peer %d is named %q", p.ID, p.Name)
		}
	}
	if fmt.Sprint(ids) != "[2 3 4 5 6]" {
		t.Fatalf("ids %v, want [2 3 4 5 6]", ids)
	}
	// All or nothing: too few free addresses change nothing.
	if out := h.run(ExitError, "peer", "add-many", "249"); !strings.Contains(out, "only 248 free address(es)") {
		t.Fatalf("too many:\n%s", out)
	}
	if n := len(h.state().Peers); n != 5 {
		t.Fatalf("%d peers after a refused add-many", n)
	}
	h.run(ExitOK, "peer", "add-many", "248")
	if n := len(h.state().Peers); n != config.MaxPeers {
		t.Fatalf("%d peers, want %d", n, config.MaxPeers)
	}
	h.run(ExitError, "peer", "add")
}

func TestChangesApplyAtRestart(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitOK, "peer", "add")
	before := h.wg0()
	h.run(ExitNeedYes, "set", "net.subnet", "10.77.0.0/24")
	if out := h.run(ExitOK, "set", "net.subnet", "10.77.0.0/24", "--yes"); !strings.Contains(out, "Saved. Restart the container to apply.") {
		t.Fatalf("set:\n%s", out)
	}
	h.run(ExitOK, "awg", "set", "jc=99", "--yes")
	h.run(ExitOK, "set", "dns.upstream", "9.9.9.9")
	if h.wg0() != before {
		t.Fatal("a command changed wg0.conf before the restart")
	}
	if b, _ := os.ReadFile(h.app.P.DNSConf()); strings.Contains(string(b), "9.9.9.9") {
		t.Fatal("a command changed the dnsmasq conf before the restart")
	}
	h.boot()
	w := h.wg0()
	if !strings.Contains(w, "Address = 10.77.0.1/24") || !strings.Contains(w, "Jc = 99") || !strings.Contains(w, "AllowedIPs = 10.77.0.2/32") {
		t.Fatalf("restart did not apply:\n%s", w)
	}
	if b, _ := os.ReadFile(h.app.P.DNSConf()); !strings.Contains(string(b), "server=9.9.9.9\n") {
		t.Fatalf("restart did not apply the dnsmasq conf:\n%s", b)
	}
	if b, _ := os.ReadFile(h.app.P.DNSAddress()); strings.TrimSpace(string(b)) != "10.77.0.1" {
		t.Fatalf("dns address %q", b)
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
	h.run(ExitOK, "peer", "add")
	h.run(ExitNeedYes, "set", "server.endpoint_host", "vpn3.example.com")
	h.run(ExitNeedYes, "server", "rotate-key")
}

func TestAWGCommands(t *testing.T) {
	h := newHarness(t)
	h.boot()
	h.run(ExitOK, "peer", "add")
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
	db, err := store.Open(h.app.P.DB())
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
	h.run(ExitOK, "peer", "add", "--name", "phone")
	for _, args := range [][]string{
		{},
		{"bogus"},
		{"init", "--bogus"},
		{"health", "--bogus"},
		{"status"},
		{"apply"},
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
		{"peer", "add", "phone2"},
		{"peer", "add", "--octet", "9"},
		{"peer", "add", "--name", " x"},
		{"peer", "add-many"},
		{"peer", "add-many", "0"},
		{"peer", "add-many", "254"},
		{"peer", "add-many", "x"},
		{"peer", "add-many", "1", "2"},
		{"peer", "list", "x"},
		{"peer", "show"},
		{"peer", "show", "phone"},
		{"peer", "show", "#2"},
		{"peer", "export"},
		{"peer", "export", "name:phone"},
		{"peer", "rename", "2"},
		{"peer", "rename", "2", "x", "--clear"},
		{"peer", "rename", "--clear"},
		{"peer", "rename", "2", "bad name!"},
		{"peer", "rename", "phone", "x"},
		{"peer", "set"},
		{"peer", "set", "2", "--keepalive", "maybe"},
		{"peer", "set", "phone", "--keepalive", "on"},
		{"peer", "enable"},
		{"peer", "disable", "phone"},
		{"peer", "rotate"},
		{"peer", "rotate", "phone", "--yes"},
		{"peer", "rm"},
		{"peer", "rm", "phone", "--yes"},
		{"import-legacy"},
		{"import-legacy", "--rename", "nope", "/x"},
		// Extra arguments, and flags of other commands.
		{"init", "x"},
		{"health", "x"},
		{"version", "x"},
		{"get", "--yes"},
		{"server", "rotate-key", "x", "--yes"},
		{"awg", "show", "x"},
		{"awg", "set", "jc=5", "--show-secrets"},
		{"awg", "regenerate", "--json"},
		{"peer", "list", "--qr"},
		{"peer", "add", "--yes"},
		{"peer", "add-many", "2", "--name", "x"},
		{"peer", "show", "2", "--yes"},
		{"peer", "enable", "2", "--keepalive", "on"},
		{"peer", "rm", "2", "3", "--yes"},
		{"import-legacy", "/x", "--yes"},
	} {
		out := h.run(ExitUsage, args...)
		if !strings.HasPrefix(out, "awgctl: ") || !strings.Contains(out, "Usage: awgctl <command>") {
			t.Errorf("awgctl %v: no usage text:\n%s", args, out)
		}
	}
	out := h.run(ExitOK, "help")
	for _, want := range []string{"Usage: awgctl", "\nSystem:\n  init                                  s6 start-up",
		"  peer export <id> [--qr] [--png FILE] [--conf FILE]\n",
		"  awg set <key>=<value>... [--yes]      (s=<n> sets S1-S4 together)\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help: no %q:\n%s", want, out)
		}
	}
	if p := h.peer(2); p.Name != "phone" || !p.Enabled {
		t.Fatal("a usage error changed the peer")
	}
}

func TestCommandsNeedSafeDatabase(t *testing.T) {
	h := newHarness(t)
	cmds := [][]string{{"get"}, {"awg", "show"}, {"peer", "list"},
		{"peer", "show", "2"}, {"peer", "export", "2"}, {"set", "net.mtu", "1300"}}
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
	os.Symlink(filepath.Join(t.TempDir(), "x"), h2.app.P.DB()+"-journal")
	h2.run(ExitError, "get")
	h2.run(ExitError, "peer", "add")
	h2.run(ExitError, "init")
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
	for _, want := range []string{"awgctl        1.2.3-r4", "awg           awg v0-test", "amneziawg-go  amneziawg-go v0-test"} {
		if !strings.Contains(out, want) {
			t.Errorf("version: no %q:\n%s", want, out)
		}
	}
	h.fake.Fail["amneziawg-go"] = "not found"
	if out := h.run(ExitOK, "version"); !strings.Contains(out, "amneziawg-go  unknown") {
		t.Fatalf("version without amneziawg-go:\n%s", out)
	}

	a := New()
	if a.P != sys.DefaultPaths() || a.Rand == nil || a.Owner == nil {
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

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		-5 * time.Second:                      "0s ago",
		0:                                     "0s ago",
		59*time.Second + 900*time.Millisecond: "59s ago",
		time.Minute:                           "1m 0s ago",
		59*time.Minute + 59*time.Second:       "59m 59s ago",
		time.Hour + 30*time.Second:            "1h 0m ago",
		50*time.Hour + 7*time.Minute:          "2d 2h ago",
	} {
		if got := ago(d); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
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
	h.run(ExitOK, "peer", "add", "--name", "phone")
	out := h.run(ExitOK, "peer", "add", "--qr", "--name", "laptop")
	if !strings.Contains(out, `Peer 3 "laptop" (the QR code holds the peer's private key; it works after the restart):`) ||
		strings.Contains(out, "Export:") {
		t.Fatalf("add --qr:\n%s", out)
	}
	h.run(ExitOK, "peer", "set", "3", "--keepalive", "off")
	if h.peer(3).Keepalive {
		t.Fatal("keepalive is still on")
	}
	if out := h.run(ExitOK, "peer", "set", "3", "--keepalive", "off"); !strings.Contains(out, "keepalive is already off") ||
		strings.Contains(out, "Saved.") {
		t.Fatalf("set the same value:\n%s", out)
	}
	for _, c := range [][]string{{"show", "9"}, {"export", "9"}, {"enable", "9"}, {"rm", "9", "--yes"}, {"rename", "9", "x"}} {
		if out := h.run(ExitError, append([]string{"peer"}, c...)...); !strings.Contains(out, "no peer with id 9") {
			t.Errorf("peer %v:\n%s", c, out)
		}
	}

	phone := h.peer(2)
	h.app.Now = func() time.Time { return time.Unix(1700000000+3*3600+25*60+9, 0) }
	h.app.R = dumpRunner{Fake: h.fake, dump: "priv\tpub\t51820\toff\n" + phone.PublicKey +
		"\tpsk\t(none)\t10.13.13.2/32\t1700000000\t1536\t3221225472\toff\nshort line\n"}
	out = h.run(ExitOK, "peer", "list")
	if !regexp.MustCompile(`(?m)^2\s+phone\s+10\.13\.13\.2\s+true\s+true\s+3h 25m ago\s+1\.5KiB/3\.0GiB$`).MatchString(out) ||
		!regexp.MustCompile(`(?m)^3\s+laptop\s+10\.13\.13\.3\s+true\s+false\s+-\s+0B/0B$`).MatchString(out) {
		t.Fatalf("list:\n%s", out)
	}
	if out := h.run(ExitOK, "peer", "list", "--json"); !strings.Contains(out, `"last_handshake": "2023-11-14T22:13:20Z"`) ||
		!strings.Contains(out, `"rx_bytes": 1536`) {
		t.Fatalf("json list:\n%s", out)
	}
	out = h.run(ExitOK, "peer", "show", "2")
	for _, want := range []string{"address     10.13.13.2/32", "preshared   true", "keepalive   true",
		"handshake   2023-11-14T22:13:20Z", "transfer    1.5KiB received, 3.0GiB sent"} {
		if !strings.Contains(out, want) {
			t.Errorf("show: no %q:\n%s", want, out)
		}
	}
	if out := h.run(ExitOK, "peer", "show", "3"); strings.Contains(out, "handshake") || strings.Contains(out, "created") {
		t.Fatalf("show without a handshake:\n%s", out)
	}
	h.app.R = h.fake

	dir := t.TempDir()
	conf, png := filepath.Join(dir, "phone.conf"), filepath.Join(dir, "phone.png")
	out = h.run(ExitOK, "peer", "export", "2", "--conf", conf, "--png", png)
	if strings.Contains(out, "[Interface]") || !strings.Contains(out, "Wrote "+conf) || !strings.Contains(out, "Wrote "+png) {
		t.Fatalf("export to files:\n%s", out)
	}
	if b, _ := os.ReadFile(conf); !strings.Contains(string(b), "Address = 10.13.13.2/32") {
		t.Fatalf("conf file:\n%s", b)
	}
	if b, _ := os.ReadFile(png); !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatal("the png file is not a PNG")
	}
	h.run(ExitError, "peer", "export", "2", "--conf", filepath.Join(dir, "no", "x.conf"))
	h.run(ExitError, "peer", "export", "2", "--png", filepath.Join(dir, "no", "x.png"))

	// An export shows the saved configuration.
	h.run(ExitOK, "set", "net.subnet", "10.77.0.0/24", "--yes")
	if out := h.run(ExitOK, "peer", "export", "2"); !strings.Contains(out, "Address = 10.77.0.2/32") {
		t.Fatalf("export after a saved change:\n%s", out)
	}
	if out := h.run(ExitOK, "peer", "export", "2", "--qr"); !strings.Contains(out, `Peer 2 "phone" (the QR code holds`) {
		t.Fatalf("export --qr:\n%s", out)
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
	if out := h.run(ExitOK, "set", "net.mtu", "1280"); !strings.Contains(out, "net.mtu is already 1280.") || strings.Contains(out, "Saved.") {
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
	if !strings.Contains(out, "New server public key: ") || !strings.Contains(out, "Saved. Restart the container to apply.") ||
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
	if out := h.run(ExitOK, "awg", "version", "1.5", "--yes"); !strings.Contains(out, "awg.i1: (removed)") ||
		!strings.Contains(out, "awg.header_protection_key: (removed)") {
		t.Fatalf("version 1.5:\n%s", out)
	}
	h.run(ExitOK, "init")
}

func TestStoreFailuresChangeNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		sql  string
		args []string
	}{
		{"settings", "CREATE TRIGGER f BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"set", "net.mtu", "1300"}},
		{"insert peer", "CREATE TRIGGER f BEFORE INSERT ON peers BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"peer", "add-many", "2"}},
		{"update peer", "CREATE TRIGGER f BEFORE UPDATE ON peers BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"peer", "disable", "2"}},
		{"delete peer", "CREATE TRIGGER f BEFORE DELETE ON peers BEGIN SELECT RAISE(ABORT, 'no'); END", []string{"peer", "rm", "2", "--yes"}},
		{"load", "ALTER TABLE peers RENAME TO gone", []string{"peer", "add"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.boot()
			h.run(ExitOK, "peer", "add")
			h.sqlExec(c.sql)
			if out := h.run(ExitError, c.args...); !strings.Contains(out, "awgctl: ") {
				t.Fatalf("output:\n%s", out)
			}
			if c.name == "load" {
				h.run(ExitError, "init")
				h.run(ExitError, "peer", "list")
				return
			}
			if st := h.state(); len(st.Peers) != 1 || !st.Peers[0].Enabled || st.Get("net.mtu") != "1280" {
				t.Fatalf("the DB changed: %+v", st)
			}
		})
	}
}

func TestInitFailures(t *testing.T) {
	h := newHarness(t)
	h.run(ExitOK, "init")
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

// Each flag of a command is registered and is in its usage line, and the
// usage line names no other flag.
func TestCommandTable(t *testing.T) {
	flagRe := regexp.MustCompile(`--[a-z-]+`)
	for _, g := range groups {
		for _, c := range g.cmds {
			want := map[string]bool{}
			for _, f := range c.flags {
				if flagDefs[f] == nil {
					t.Errorf("%s: flag %q is not in flagDefs", c.name, f)
				}
				want["--"+f] = true
			}
			got := map[string]bool{}
			for _, f := range flagRe.FindAllString(c.args, -1) {
				got[f] = true
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%s: the usage line names %v, the command accepts %v", c.name, got, want)
			}
			if c.run == nil {
				t.Errorf("%s: no handler", c.name)
			}
		}
	}
}

func TestKernelModuleCheck(t *testing.T) {
	for v, want := range map[string][3]int{"3.1.20250101": {3, 1, 1}, "2.0": {2, 0, 1}, "10.12.x": {10, 12, 1}} {
		major, minor, ok := parseModuleVersion(v)
		if major != want[0] || minor != want[1] || !ok {
			t.Errorf("parseModuleVersion(%q) = %d %d %v", v, major, minor, ok)
		}
	}
	for _, v := range []string{"", "3", "x.1", "3.y"} {
		if _, _, ok := parseModuleVersion(v); ok {
			t.Errorf("parseModuleVersion(%q) accepted", v)
		}
	}

	h := newHarness(t) // AWG 3.1: RandomTrailers is on
	os.MkdirAll(filepath.Dir(h.app.P.ModuleVersion()), 0o755)
	const warning = "need amneziawg kernel module 3.1 or newer"
	for v, warn := range map[string]bool{"3.0.20250101\n": true, "1.0.0": true, "3.1.20250101": false, "4.0": false} {
		os.WriteFile(h.app.P.ModuleVersion(), []byte(v), 0o644)
		if out := h.run(ExitOK, "init"); strings.Contains(out, warning) != warn {
			t.Errorf("module %q: warning %v, want %v:\n%s", v, !warn, warn, out)
		}
	}
	os.WriteFile(h.app.P.ModuleVersion(), []byte("3.0.0"), 0o644)
	h.env["WG_QUICK_USERSPACE_IMPLEMENTATION"] = "amneziawg-go"
	if out := h.run(ExitOK, "init"); strings.Contains(out, warning) {
		t.Errorf("userspace: the kernel module warning is shown:\n%s", out)
	}
}
