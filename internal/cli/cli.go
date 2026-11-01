// Package cli implements the awgctl commands.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/apply"
	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/lock"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
)

// Exit codes.
const (
	ExitOK         = 0
	ExitError      = 1 // nothing changed
	ExitUsage      = 2
	ExitNeedYes    = 3
	ExitNotApplied = 4 // saved, not applied
)

// Version is set at build time with -ldflags "-X .../cli.Version=...".
var Version = "dev"

// App is awgctl with its outside world.
type App struct {
	P       sys.Paths
	R       sys.Runner
	Out     io.Writer
	Err     io.Writer
	Getenv  seed.Getenv
	Detect  seed.Detector
	Rand    *rand.Rand
	Owner   func() (fsafe.Owner, error)
	LockTTL time.Duration
}

// New returns the real App.
func New() *App {
	a := &App{
		P:   sys.DefaultPaths(),
		R:   sys.Exec{},
		Out: os.Stdout,
		Err: os.Stderr,
		Getenv: func(k string) (string, bool) {
			v, ok := os.LookupEnv(k)
			return v, ok && v != ""
		},
		Rand:    awg.NewRand(),
		Owner:   fsafe.LookupOwner,
		LockTTL: 2 * time.Minute,
	}
	a.Detect = seed.Retrying(func(ctx context.Context) (string, error) {
		return seed.DetectIPv4(ctx, seed.DetectURLs, nil)
	}, func(s string) { a.logf("%s", s) })
	return a
}

func (a *App) env() apply.Env { return apply.Env{P: a.P, R: a.R} }

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(a.Out, "**** "+format+" ****\n", args...)
}

func (a *App) warnf(format string, args ...any) {
	fmt.Fprintf(a.Out, "**** WARNING: "+format+" ****\n", args...)
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func usagef(format string, args ...any) error {
	return &exitError{ExitUsage, fmt.Errorf(format, args...)}
}

func needYes(format string, args ...any) error {
	return &exitError{ExitNeedYes, fmt.Errorf(format, args...)}
}

const usage = `Usage: awgctl <command> [arguments]

System:
  init                                  s6 start-up: seed or check the DB, render /run
  health                                Docker health check
  status                                tunnel, peers, pending restart and apply state
  apply                                 apply the saved state again (after exit code 4)
  version                               versions of awgctl, awg, amneziawg-go and the schema

Settings (not AWG):
  get [key] [--show-secrets]
  set <key> <value> [--yes]
  reset <key> [--yes]
  server rotate-key --yes

AmneziaWG parameters:
  awg show [--show-secrets]
  awg version <1.5|2.0|3.0|3.1> --yes
  awg set <key>=<value>... [--yes]        (s=<n> sets S1-S4 together)
  awg regenerate [keys...] --yes

Peers (<peer> = id, #id, name, or name:<digits>):
  peer add <name> [--no-keepalive] [--octet N] [--qr]
  peer list [--json]
  peer show <peer>
  peer export <peer> [--qr | --png FILE | --conf FILE]
  peer rename <peer> <new-name>
  peer set <peer> --keepalive on|off
  peer disable <peer> | peer enable <peer>
  peer rotate <peer> --yes
  peer rm <peer> --yes

Migration:
  import-legacy <dir> [--dry-run] [--force-partial] [--rename OLD=NEW]...

Exit codes: 0 done, 1 error (nothing changed), 2 usage, 3 refused without --yes,
4 saved but not applied (run awgctl apply or restart the container).
`

// Main runs one command and returns the exit code.
func (a *App) Main(args []string) int {
	ctx := context.Background()
	err := a.dispatch(ctx, args)
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	code := ExitError
	if errors.As(err, &ee) {
		code = ee.code
	}
	if code == ExitUsage {
		fmt.Fprintf(a.Err, "awgctl: %v\n\n%s", err, usage)
	} else {
		fmt.Fprintf(a.Err, "awgctl: %v\n", err)
	}
	return code
}

func (a *App) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("no command")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(a.Out, usage)
		return nil
	case "init":
		return a.cmdInit(ctx, rest)
	case "health":
		return a.cmdHealth(ctx, rest)
	case "status":
		return a.cmdStatus(ctx, rest)
	case "apply":
		return a.cmdApply(ctx, rest)
	case "version":
		return a.cmdVersion(ctx, rest)
	case "get":
		return a.cmdGet(ctx, rest)
	case "set":
		return a.cmdSet(ctx, rest)
	case "reset":
		return a.cmdReset(ctx, rest)
	case "server":
		return a.cmdServer(ctx, rest)
	case "awg":
		return a.cmdAWG(ctx, rest)
	case "peer":
		return a.cmdPeer(ctx, rest)
	case "import-legacy":
		return a.cmdImportLegacy(ctx, rest)
	}
	return usagef("unknown command %q", cmd)
}

// parse parses flags that may come before, between or after the positional
// arguments ("peer add phone --qr").
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usagef("%v", err)
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// openDB opens the existing DB after the safety checks.
func (a *App) openDB() (*store.DB, error) {
	o, err := a.Owner()
	if err != nil {
		return nil, fmt.Errorf("volume owner: %w", err)
	}
	if err := fsafe.CheckConfig(a.P.Config, a.P.DB(), a.P.Backups(), false, o); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(a.P.DB()); err != nil {
		return nil, fmt.Errorf("no database at %s: the container has not finished its first start", a.P.DB())
	}
	return store.Open(a.P.DB(), a.P.Backups())
}

func (a *App) view(ctx context.Context) (*config.State, error) {
	db, err := a.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var st *config.State
	err = db.View(ctx, func(tx *store.Tx) error {
		var err error
		st, err = config.Load(tx)
		return err
	})
	return st, err
}

func (a *App) lock(ctx context.Context) (*lock.Lock, error) {
	ctx, cancel := context.WithTimeout(ctx, a.LockTTL)
	defer cancel()
	return lock.Acquire(ctx, a.P.Lock())
}

// plan is what a mutation reports before the commit.
type plan struct {
	class   config.Class
	reissue string // who needs a new conf ("" = nobody)
	summary []string
}

// change runs one admin command: lock, load, mutate in memory, refuse without
// --yes when peers need new confs, validate everything with the real parsers,
// commit, then apply and record the result.
func (a *App) change(ctx context.Context, yes bool, mutate func(*config.State) (plan, error)) error {
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

	var pl plan
	var gen int64
	err = db.Update(ctx, func(tx *store.Tx) error {
		old, err := config.Load(tx)
		if err != nil {
			return err
		}
		d := old.Clone()
		if pl, err = mutate(d); err != nil {
			return err
		}
		if pl.reissue != "" && !yes {
			return needYes("%s needs a new conf after this change; run again with --yes", pl.reissue)
		}
		if err := a.env().Check(ctx, d); err != nil {
			return err
		}
		if err := d.SaveSettings(tx); err != nil {
			return err
		}
		if err := syncPeers(tx, old, d); err != nil {
			return err
		}
		gen, err = tx.BumpGeneration()
		return err
	})
	if err != nil {
		return err
	}
	for _, s := range pl.summary {
		fmt.Fprintln(a.Out, s)
	}
	return a.applySaved(ctx, db, gen, pl)
}

// applySaved applies the committed generation and records the result.
func (a *App) applySaved(ctx context.Context, db *store.DB, gen int64, pl plan) error {
	var d *config.State
	if err := db.View(ctx, func(tx *store.Tx) error {
		var err error
		d, err = config.Load(tx)
		return err
	}); err != nil {
		return &exitError{ExitNotApplied, fmt.Errorf("saved, not applied: %w", err)}
	}
	e := a.env()
	active, aerr := e.ReadActive()
	var pending []string
	if aerr == nil {
		pending = config.PendingRestart(d, active)
	}
	applyErr := aerr
	if applyErr == nil {
		applyErr = e.Live(ctx, d)
	}
	if err := e.Record(gen, applyErr, pending); err != nil {
		fmt.Fprintf(a.Err, "awgctl: could not write the apply state: %v\n", err)
	}
	switch {
	case applyErr == nil:
		if pl.class.Has(config.Live) || pl.class.Has(config.DNS) {
			fmt.Fprintln(a.Out, "Applied.")
		}
	case errors.Is(applyErr, apply.ErrNotUp):
		fmt.Fprintln(a.Out, "Saved. wg0 is not up; the change is applied when the tunnel starts.")
	default:
		return &exitError{ExitNotApplied, fmt.Errorf("saved, not applied: %v. Run awgctl apply or restart the container", applyErr)}
	}
	if len(pending) > 0 {
		fmt.Fprintf(a.Out, "Pending restart: %s. Restart the container to apply.\n", strings.Join(pending, ", "))
	}
	if pl.reissue != "" {
		fmt.Fprintf(a.Out, "New conf needed for %s: awgctl peer export <peer> --qr\n", pl.reissue)
	}
	return nil
}

// syncPeers writes the peer changes between old and d: new peers (ID 0) are
// inserted, missing ones deleted, changed ones updated.
func syncPeers(tx *store.Tx, old, d *config.State) error {
	byID := map[int64]store.Peer{}
	for _, p := range old.Peers {
		byID[p.ID] = p
	}
	keep := map[int64]bool{}
	for i, p := range d.Peers {
		if p.ID == 0 {
			id, err := tx.AddPeer(p)
			if err != nil {
				return err
			}
			d.Peers[i].ID = id
			continue
		}
		keep[p.ID] = true
		if o := byID[p.ID]; o != p {
			if err := tx.UpdatePeer(p); err != nil {
				return err
			}
		}
	}
	for id := range byID {
		if !keep[id] {
			if err := tx.DeletePeer(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func allPeers(d *config.State) string {
	if len(d.Peers) == 0 {
		return ""
	}
	return "every peer"
}
