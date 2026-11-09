package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/health"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
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

	o, err := a.Owner()
	if err != nil {
		return fmt.Errorf("volume owner: %w", err)
	}
	// A PUID/PGID change leaves the volume owned by the old IDs; ChownTree
	// below gives it to the new ones.
	if err := fsafe.CheckConfig(a.P.Config, a.P.DB(), true, o); err != nil {
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

	db, err := store.Open(a.P.DB())
	if err != nil {
		return err
	}
	defer db.Close()
	// One read transaction from the load to the last runtime file: a command
	// cannot commit in between, so this start renders one consistent state.
	var st *config.State
	if err := db.View(ctx, func(tx *store.Tx) error {
		if st, err = config.Load(tx); err != nil {
			return err
		}
		return a.render(ctx, st)
	}); err != nil {
		return err
	}

	if err := fsafe.Harden(a.P.Config); err != nil {
		a.warnf("could not set the modes in /config: %v", err)
	}
	if err := fsafe.ChownTree(a.P.Config, o); err != nil {
		a.warnf("could not set the owner of everything in /config (read-only or remote volume?): %v", err)
	}
	a.logf("AmneziaWG %s, endpoint %s:%s, subnet %s, %d peer(s)", st.AWG["version"],
		st.Get(config.KeyEndpointHost), st.Get(config.KeyEndpointPort), st.Subnet(), len(st.Peers))
	if len(st.Peers) == 0 {
		a.logf("No peers yet. Add one with: docker exec -it <container> awgctl peer add --qr, then restart the container")
	}
	a.logf("Config initialization finished")
	return nil
}

// render checks st completely and writes the runtime files: wg0.conf, the
// dnsmasq conf and the dnsmasq address.
func (a *App) render(ctx context.Context, st *config.State) error {
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
	a.checkKernel(st)
	if err := a.env().Check(ctx, st); err != nil {
		return fmt.Errorf("config check failed: %w", err)
	}
	srv, err := render.Server(st)
	if err != nil {
		return err
	}
	if err := sys.WriteAtomic(a.P.WG0(), []byte(srv.String()), 0o600); err != nil {
		return err
	}
	if err := sys.WriteAtomic(a.P.DNSConf(), []byte(render.Dnsmasq(st.Get(config.KeyDNSUpstream))), 0o644); err != nil {
		return err
	}
	return sys.WriteAtomic(a.P.DNSAddress(), []byte(config.ServerAddress(st.Subnet())+"\n"), 0o644)
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
	err = store.Create(a.P.DB(), func(tx *store.Tx) error { return st.Save(tx, nil) })
	if err != nil {
		return fmt.Errorf("could not create the database: %w", err)
	}
	a.logf("Created %s (AWG %s)", a.P.DB(), st.AWG["version"])
	return nil
}

// checkKernel warns when the 3.1 switches meet a kernel module older than 3.1.
// Only major.minor are read: the trailer fix did not change the version date.
func (a *App) checkKernel(st *config.State) {
	if !st.AWG.On(awg.RandomTrailers) && !st.AWG.On(awg.DisableCookies) {
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
	if err := health.Check(ctx, a.P, a.R); err != nil {
		fmt.Fprintln(a.Out, err)
		return &exitError{ExitError, err}
	}
	fmt.Fprintln(a.Out, "healthy")
	return nil
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
	return nil
}
