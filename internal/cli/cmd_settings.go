package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
)

const masked = "******** (secret; --show-secrets)"

func (a *App) cmdGet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	show := fs.Bool("show-secrets", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("get takes at most one key")
	}
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	if len(pos) == 1 {
		k := pos[0]
		if strings.HasPrefix(k, "awg.") {
			return usagef("AWG values: awgctl awg show")
		}
		s, ok := config.Lookup(k)
		if !ok {
			return usagef("unknown setting %q", k)
		}
		v := st.Get(k)
		if s.Secret && !*show {
			v = masked
		}
		fmt.Fprintln(a.Out, v)
		return nil
	}
	for _, s := range config.Registry {
		v := st.Get(s.Key)
		note := ""
		if s.Secret && !*show {
			v = masked
		} else if s.Default != "" && v == s.Default {
			note = "  (default)"
		}
		fmt.Fprintf(a.Out, "%-26s %s%s\n", s.Key, v, note)
	}
	return nil
}

// settable returns the registry entry of a key that set/reset may change.
func settable(k string) (config.Setting, error) {
	if strings.HasPrefix(k, "awg.") || awg.KeyForConf(k) != "" {
		return config.Setting{}, usagef("AWG values change only through awgctl awg (for example: awgctl awg set %s=<value>)", strings.TrimPrefix(k, "awg."))
	}
	s, ok := config.Lookup(k)
	if !ok {
		var keys []string
		for _, r := range config.Registry {
			if r.Settable {
				keys = append(keys, r.Key)
			}
		}
		return s, usagef("unknown setting %q (settable: %s)", k, strings.Join(keys, ", "))
	}
	if !s.Settable {
		return s, usagef("%s cannot be set: %s", k, s.Help)
	}
	return s, nil
}

func (a *App) setOne(ctx context.Context, k, v string, yes bool) error {
	s, err := settable(k)
	if err != nil {
		return err
	}
	n, err := s.Check(v)
	if err != nil {
		return fmt.Errorf("%s: %v", k, err)
	}
	return a.change(ctx, yes, func(d *config.State) (plan, error) {
		old := d.Get(k)
		if old == n {
			return plan{noop: true, summary: []string{fmt.Sprintf("%s is already %s.", k, n)}}, nil
		}
		d.Settings[k] = n
		pl := plan{summary: []string{fmt.Sprintf("%s: %s -> %s", k, old, n)}}
		if s.Reissue {
			pl.reissue = allPeers(d)
		}
		return pl, nil
	})
}

func (a *App) cmdSet(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("set", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("usage: awgctl set <key> <value> [--yes]")
	}
	return a.setOne(ctx, pos[0], pos[1], *yes)
}

func (a *App) cmdReset(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: awgctl reset <key> [--yes]")
	}
	s, err := settable(pos[0])
	if err != nil {
		return err
	}
	if s.Default == "" {
		return usagef("%s has no built-in default; use awgctl set", s.Key)
	}
	return a.setOne(ctx, s.Key, s.Default, *yes)
}

func (a *App) cmdServer(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "rotate-key" {
		return usagef("usage: awgctl server rotate-key --yes")
	}
	return a.change(ctx, *yes, func(d *config.State) (plan, error) {
		d.Settings["server.private_key"] = keys.NewPrivate()
		pub, _ := keys.Public(d.Settings["server.private_key"])
		return plan{reissue: allPeersOrServer(d),
			summary: []string{"New server public key: " + pub}}, nil
	})
}

// allPeersOrServer always asks for --yes: a new server key cuts every peer off.
func allPeersOrServer(d *config.State) string {
	if r := allPeers(d); r != "" {
		return r
	}
	return "the server (no peers yet)"
}
