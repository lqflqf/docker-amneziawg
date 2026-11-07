package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/legacy"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

type renameFlag map[string]string

func (r renameFlag) String() string { return "" }
func (r renameFlag) Set(v string) error {
	old, n, ok := strings.Cut(v, "=")
	if !ok || old == "" || n == "" {
		return fmt.Errorf("--rename takes OLD=NEW")
	}
	r[old] = n
	return nil
}

// keepInConfig are the entries of /config that are not legacy files.
func keepInConfig(name string) bool {
	return name == "amneziawg.db" || strings.HasPrefix(name, "legacy-") ||
		strings.HasPrefix(name, ".amneziawg.db")
}

func (a *App) cmdImportLegacy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("import-legacy", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "")
	force := fs.Bool("force-partial", false, "")
	ren := renameFlag{}
	fs.Var(ren, "rename", "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: awgctl import-legacy <dir> [--dry-run] [--force-partial] [--rename OLD=NEW]...")
	}
	dir, err := filepath.Abs(pos[0])
	if err != nil {
		return err
	}
	if _, err := os.Lstat(a.P.DB()); err == nil {
		return fmt.Errorf("%s exists: import-legacy runs only once, into an empty volume", a.P.DB())
	}
	rep, err := legacy.Analyze(legacy.Options{Dir: dir, ForcePartial: *force, Rename: ren})
	if err != nil {
		return err
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(a.Out, "note:     %s\n", n)
	}
	for _, d := range rep.Dropped {
		fmt.Fprintf(a.Out, "drop:     %s\n", d)
	}
	for _, b := range rep.Blockers {
		fmt.Fprintf(a.Out, "BLOCKER:  %s\n", b)
	}
	if len(rep.Blockers) > 0 {
		return fmt.Errorf("%d item(s) stop the import (see BLOCKER above); nothing was written", len(rep.Blockers))
	}
	st := rep.State
	fmt.Fprintf(a.Out, "server:   endpoint %s:%s, subnet %s, AWG %s, %d peer(s)\n",
		st.Get("server.endpoint_host"), st.Get("server.endpoint_port"), st.Subnet(), st.AWG["version"], len(st.Peers))
	same := true
	for _, v := range rep.Verdicts {
		line := fmt.Sprintf("peer:     %-20s %s", v.Name, v.Result)
		if len(v.Changed) > 0 {
			line += " (" + strings.Join(v.Changed, ", ") + ") -> export a new conf for this peer"
		}
		if v.Result != "same" {
			same = false
		}
		fmt.Fprintln(a.Out, line)
	}
	if len(rep.Server) > 0 {
		fmt.Fprintf(a.Out, "server:   wg0.conf differs in %s\n", strings.Join(rep.Server, ", "))
	}
	if same {
		fmt.Fprintln(a.Out, "result:   every peer conf is unchanged: no peer needs a new conf")
	}
	if len(rep.Dropped) > 0 && !*force {
		return fmt.Errorf("%d item(s) cannot be imported (see drop above). Fix them on the old image, or import without them with --force-partial", len(rep.Dropped))
	}
	if *dry {
		fmt.Fprintln(a.Out, "dry run:  nothing was written")
		return nil
	}
	if err := a.env().Check(ctx, st); err != nil {
		return fmt.Errorf("the imported config does not pass the checks: %w", err)
	}
	err = store.Create(a.P.DB(), func(tx *store.Tx) error {
		if err := st.SaveSettings(tx); err != nil {
			return err
		}
		for _, p := range st.Peers {
			if err := tx.AddPeer(p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("could not write the database: %w", err)
	}
	fmt.Fprintf(a.Out, "imported: %s\n", a.P.DB())
	if filepath.Clean(dir) == filepath.Clean(a.P.Config) {
		// The database is saved: a failed move is only a warning, because a
		// second run refuses the existing database.
		if dst, err := a.archiveLegacy(); err != nil {
			fmt.Fprintf(a.Out, "WARNING:  the old files were not all moved (%v). Move the rest out of %s by hand\n", err, a.P.Config)
		} else {
			fmt.Fprintf(a.Out, "moved:    the old files to %s\n", dst)
		}
	}
	fmt.Fprintln(a.Out, "next:     start the container as usual")
	return nil
}

// archiveLegacy moves the old files of /config into a new legacy-<time> directory.
func (a *App) archiveLegacy() (string, error) {
	dst := filepath.Join(a.P.Config, "legacy-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Mkdir(dst, 0o700); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(a.P.Config)
	for _, e := range entries {
		if keepInConfig(e.Name()) {
			continue
		}
		if err := os.Rename(filepath.Join(a.P.Config, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return "", fmt.Errorf("move %s to %s: %w", e.Name(), dst, err)
		}
	}
	return dst, nil
}
