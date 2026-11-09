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

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/check"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/fsafe"
	"github.com/lqflqf/docker-amneziawg/internal/seed"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitError   = 1 // nothing changed
	ExitUsage   = 2
	ExitNeedYes = 3
)

// Version is set at build time with -ldflags "-X .../cli.Version=...".
var Version = "dev"

// App is awgctl with its outside world.
type App struct {
	P      sys.Paths
	R      sys.Runner
	Out    io.Writer
	Err    io.Writer
	Getenv seed.Getenv
	Detect seed.Detector
	Rand   *rand.Rand
	Owner  func() (fsafe.Owner, error)
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
		Rand:  awg.NewRand(),
		Owner: fsafe.LookupOwner,
	}
	a.Detect = seed.Retrying(func(ctx context.Context) (string, error) {
		return seed.DetectIPv4(ctx, seed.DetectURLs, nil)
	}, func(s string) { a.logf("%s", s) })
	return a
}

func (a *App) env() check.Env { return check.Env{R: a.R} }

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

Each change is saved in the database. Restart the container to apply it.

System:
  init                                  s6 start-up: create or check the DB, render /run
  health                                Docker health check
  version                               versions of awgctl, awg and amneziawg-go

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

Peers (<id> = the peer id, a number 2-254, also the last octet of its address):
  peer add [--name NAME] [--no-keepalive] [--qr]
  peer add-many <count> [--no-keepalive]
  peer list [--json]
  peer show <id>
  peer export <id> [--qr | --png FILE | --conf FILE]
  peer rename <id> <new-name> | peer rename <id> --clear
  peer set <id> --keepalive on|off
  peer disable <id> | peer enable <id>
  peer rotate <id> --yes
  peer rm <id> --yes

Migration (run with: docker run --rm --entrypoint awgctl -v <dir>:/config <image>):
  import-legacy <dir> [--dry-run] [--force-partial] [--rename OLD=NEW]...

Exit codes: 0 done, 1 error (nothing changed), 2 usage, 3 refused without --yes.
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
	if err := fsafe.CheckConfig(a.P.Config, a.P.DB(), false, o); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(a.P.DB()); err != nil {
		return nil, fmt.Errorf("no database at %s: the container has not finished its first start", a.P.DB())
	}
	return store.Open(a.P.DB())
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

// plan is what a mutation reports before the commit.
type plan struct {
	noop    bool   // nothing changes; nothing is saved
	reissue string // who needs a new conf ("" = nobody)
	summary []string
}

// errNoop rolls back a mutation that changes nothing.
var errNoop = errors.New("no change")

// change runs one admin command in one write transaction: load, mutate in
// memory, refuse without --yes when peers need new confs, check everything
// with the real parsers, commit. The running system does not change: the
// next container start applies the saved configuration.
func (a *App) change(ctx context.Context, yes bool, mutate func(*config.State) (plan, error)) error {
	db, err := a.openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	var pl plan
	err = db.Update(ctx, func(tx *store.Tx) error {
		old, err := config.Load(tx)
		if err != nil {
			return err
		}
		d := old.Clone()
		if pl, err = mutate(d); err != nil {
			return err
		}
		if pl.noop {
			return errNoop
		}
		if pl.reissue != "" && !yes {
			return needYes("%s needs a new conf after this change; run again with --yes", pl.reissue)
		}
		if err := a.env().Check(ctx, d); err != nil {
			return err
		}
		return d.Save(tx, old)
	})
	if err != nil && !errors.Is(err, errNoop) {
		return err
	}
	for _, s := range pl.summary {
		fmt.Fprintln(a.Out, s)
	}
	if pl.noop {
		return nil
	}
	if pl.reissue != "" {
		fmt.Fprintf(a.Out, "New conf needed for %s after the restart: awgctl peer export <id> --qr\n", pl.reissue)
	}
	fmt.Fprintln(a.Out, "Saved. Restart the container to apply.")
	return nil
}

func allPeers(d *config.State) string {
	if len(d.Peers) == 0 {
		return ""
	}
	return "every peer"
}
