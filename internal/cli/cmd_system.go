package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/apply"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/health"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

// legacyMarkers are files of the shell-based releases.
var legacyMarkers = []string{".donoteditthisfile", "wg_confs", "server", "templates", "awg0.conf", "wg0.conf"}

func (a *App) legacyFiles() []string {
	var found []string
	for _, m := range legacyMarkers {
		if _, err := os.Lstat(filepath.Join(a.P.Config, m)); err == nil {
			found = append(found, m)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(a.P.Config, "peer*"))
	for _, m := range matches {
		if fi, err := os.Lstat(m); err == nil && fi.IsDir() {
			found = append(found, filepath.Base(m))
		}
	}
	return found
}

// cmdInit runs at each container start (s6 oneshot). Any error is fatal: the
// s6 script stops the container, and the restart policy tries again.
func (a *App) cmdInit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	// Stale runtime state from before a docker restart (/run survives it).
	for _, p := range []string{a.P.Settled(), a.P.DNSState()} {
		os.Remove(p)
	}
	os.RemoveAll(a.P.RunDir())
	if err := os.MkdirAll(a.P.RunDir(), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(a.P.DNSDir(), 0o755); err != nil {
		return err
	}
	l, err := a.lock(ctx)
	if err != nil {
		return err
	}
	defer l.Release()

	o, err := a.Owner()
	if err != nil {
		return fmt.Errorf("volume owner: %w", err)
	}
	// A PUID/PGID change leaves the volume owned by the old IDs; ChownTree
	// below gives it to the new ones.
	if err := fsafe.CheckConfig(a.P.Config, a.P.DB(), a.P.Backups(), true, o); err != nil {
		return err
	}
	if _, err := os.Lstat(a.P.DB()); os.IsNotExist(err) {
		if old := a.legacyFiles(); len(old) > 0 {
			return fmt.Errorf("/config holds files of an older release (%s) and no database. Import them once with: "+
				"docker run --rm --entrypoint awgctl -v <your config dir>:/config <image> import-legacy /config "+
				"(add --dry-run first), or move the old files away", strings.Join(old, ", "))
		}
		if err := a.seedDB(ctx); err != nil {
			return err
		}
	}

	db, err := store.Open(a.P.DB(), a.P.Backups())
	if err != nil {
		if errors.Is(err, store.ErrNewerSchema) {
			return fmt.Errorf("%v: use that image, or restore a backup made by this one", err)
		}
		return err
	}
	defer db.Close()
	var st *config.State
	if err := db.View(ctx, func(tx *store.Tx) error {
		st, err = config.Load(tx)
		return err
	}); err != nil {
		return err
	}
	errs, warns := st.Validate()
	for _, w := range warns {
		a.warnf("%s", w)
	}
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(a.Out, "**** ERROR: %v ****\n", e)
		}
		return fmt.Errorf("the database holds invalid values (see above)")
	}
	for _, d := range seed.Drift(st, a.Getenv) {
		a.warnf("%s", d)
	}
	a.checkKernel(st)

	e := a.env()
	if err := e.Check(ctx, st); err != nil {
		return fmt.Errorf("config check failed: %w", err)
	}
	active := config.ActiveOf(st)
	if err := e.WriteActive(active); err != nil {
		return err
	}
	if _, err := e.WriteRuntime(st, active); err != nil {
		return err
	}
	if err := os.WriteFile(a.P.DNSAddress(), []byte(config.ServerAddress(active["net.subnet"])+"\n"), 0o644); err != nil {
		return err
	}
	if err := e.WriteState(apply.State{Committed: st.Generation, Applied: st.Generation}); err != nil {
		return err
	}

	if err := fsafe.Harden(a.P.Config); err != nil {
		a.warnf("could not set the modes in /config: %v", err)
	}
	if err := fsafe.ChownTree(a.P.Config, o); err != nil {
		a.warnf("could not set the owner of everything in /config (read-only or remote volume?): %v", err)
	}
	a.logf("AmneziaWG %s, endpoint %s:%s, subnet %s, %d peer(s)", st.AWG["version"],
		st.Get("server.endpoint_host"), st.Get("server.endpoint_port"), st.Subnet(), len(st.Peers))
	if len(st.Peers) == 0 {
		a.logf("No peers yet. Add one with: docker exec -it <container> awgctl peer add <name> --qr")
	}
	a.logf("Config initialization finished")
	return nil
}

func (a *App) seedDB(ctx context.Context) error {
	a.logf("No database: creating it from the environment (first start)")
	st, notes, err := seed.Build(ctx, a.Rand, a.Getenv, a.Detect)
	if err != nil {
		return err
	}
	for _, n := range notes {
		a.logf("%s", n)
	}
	if err := a.env().Check(ctx, st); err != nil {
		return fmt.Errorf("the seeded config is not valid: %w", err)
	}
	err = store.Create(a.P.DB(), func(tx *store.Tx) error {
		if err := st.SaveSettings(tx); err != nil {
			return err
		}
		for k, v := range map[string]string{
			"created_at":  time.Now().UTC().Format(time.RFC3339),
			"created_by":  Version,
			"seeded_from": "env",
			"generation":  "1",
		} {
			if err := tx.SetMeta(k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("could not create the database: %w", err)
	}
	a.logf("Created %s (AWG %s)", a.P.DB(), st.AWG["version"])
	return nil
}

// checkKernel warns when the 3.1 switches meet a kernel module older than 3.1.
// Only major.minor are read: the trailer fix did not change the version date.
func (a *App) checkKernel(st *config.State) {
	if !st.AWG.On("random_trailers") && !st.AWG.On("disable_cookies") {
		return
	}
	if _, ok := a.Getenv("WG_QUICK_USERSPACE_IMPLEMENTATION"); ok {
		return
	}
	b, err := os.ReadFile("/sys/module/amneziawg/version")
	if err != nil {
		return
	}
	v := strings.TrimSpace(string(b))
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return
	}
	if major < 3 || major == 3 && minor < 1 {
		a.warnf("RandomTrailers/DisableCookies need amneziawg kernel module 3.1 or newer, but %s is loaded. "+
			"Upgrade the host module, or turn the switches off with: awgctl awg set random_trailers=off disable_cookies=off --yes", v)
	}
}

func (a *App) cmdHealth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if err := health.Check(ctx, a.env()); err != nil {
		fmt.Fprintln(a.Out, err)
		return &exitError{ExitError, err}
	}
	fmt.Fprintln(a.Out, "healthy")
	return nil
}

func (a *App) cmdApply(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	l, err := a.lock(ctx)
	if err != nil {
		return err
	}
	defer l.Release()
	db, err := a.openDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var gen int64
	if err := db.View(ctx, func(tx *store.Tx) error {
		gen, err = tx.Generation()
		return err
	}); err != nil {
		return err
	}
	return a.applySaved(ctx, db, gen, plan{class: config.Live})
}

func (a *App) cmdVersion(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "awgctl        %s\n", Version)
	for _, c := range [][]string{{"awg", "--version"}, {"amneziawg-go", "--version"}} {
		out, _, err := a.R.Run(ctx, c[0], c[1:], nil, nil)
		v := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if err != nil || v == "" {
			v = "unknown"
		}
		fmt.Fprintf(a.Out, "%-13s %s\n", c[0], v)
	}
	fmt.Fprintf(a.Out, "schema        %d (this binary)\n", store.SchemaVersion)
	return nil
}

func (a *App) cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	e := a.env()
	up := "down"
	if e.Up(ctx) {
		up = "up"
	}
	datapath := "kernel module"
	if _, ok := a.Getenv("WG_QUICK_USERSPACE_IMPLEMENTATION"); ok {
		datapath = "userspace (amneziawg-go)"
	} else if b, err := os.ReadFile("/run/s6/container_environment/WG_QUICK_USERSPACE_IMPLEMENTATION"); err == nil && len(b) > 0 {
		datapath = "userspace (amneziawg-go)"
	}
	pub, _ := keys.Public(st.Get("server.private_key"))
	fmt.Fprintf(a.Out, "tunnel      wg0 %s (%s)\n", up, datapath)
	fmt.Fprintf(a.Out, "endpoint    %s:%s\n", st.Get("server.endpoint_host"), st.Get("server.endpoint_port"))
	fmt.Fprintf(a.Out, "subnet      %s\n", st.Subnet())
	fmt.Fprintf(a.Out, "public key  %s\n", pub)
	fmt.Fprintf(a.Out, "awg         %s (RandomTrailers %s, DisableCookies %s)\n", st.AWG["version"], st.AWG["random_trailers"], st.AWG["disable_cookies"])
	enabled := 0
	for _, p := range st.Peers {
		if p.Enabled {
			enabled++
		}
	}
	fmt.Fprintf(a.Out, "peers       %d (%d enabled)\n", len(st.Peers), enabled)
	if active, err := e.ReadActive(); err == nil {
		if pend := config.PendingRestart(st, active); len(pend) > 0 {
			fmt.Fprintf(a.Out, "pending     restart for: %s\n", strings.Join(pend, ", "))
		}
	}
	if s, err := e.ReadState(); err == nil && s.Pending() {
		msg := fmt.Sprintf("generation %d saved, %d applied", s.Committed, s.Applied)
		if s.ApplyError != "" {
			msg += ": " + s.ApplyError
		}
		fmt.Fprintf(a.Out, "apply       %s (run awgctl apply)\n", msg)
	}
	return nil
}
