package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
)

func (a *App) cmdAWG(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("usage: awgctl awg show|version|set|regenerate")
	}
	fs := flag.NewFlagSet("awg", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "")
	show := fs.Bool("show-secrets", false, "")
	sub := args[0]
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	switch sub {
	case "show":
		st, err := a.view(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "# AWG %s\n", st.AWG["version"])
		for _, l := range awg.Lines(st.AWG) {
			v := l.Value
			if awg.Secret(awg.KeyForConf(l.Key)) && !*show {
				v = masked
			}
			fmt.Fprintf(a.Out, "%s = %s\n", l.Key, v)
		}
		return nil
	case "version":
		if len(pos) != 1 {
			return usagef("usage: awgctl awg version <%s> --yes", strings.Join(awg.Versions, "|"))
		}
		return a.awgChange(ctx, *yes, func(p awg.Params) (awg.Params, error) {
			return awg.Transition(a.Rand, p, pos[0])
		})
	case "set":
		if len(pos) == 0 {
			return usagef("usage: awgctl awg set <key>=<value>... [--yes]")
		}
		var kv [][2]string
		for _, s := range pos {
			k, v, ok := strings.Cut(s, "=")
			if !ok {
				return usagef("%q is not key=value", s)
			}
			kv = append(kv, [2]string{k, v})
		}
		return a.awgChange(ctx, *yes, func(p awg.Params) (awg.Params, error) {
			return awg.Set(p, kv)
		})
	case "regenerate":
		return a.awgChange(ctx, *yes, func(p awg.Params) (awg.Params, error) {
			return awg.Regenerate(a.Rand, p, pos)
		})
	}
	return usagef("unknown awg command %q", sub)
}

// awgChange applies f to the AWG set. Every AWG change is restart-class; a
// change to a value that must match on every peer also needs new peer confs.
func (a *App) awgChange(ctx context.Context, yes bool, f func(awg.Params) (awg.Params, error)) error {
	return a.change(ctx, yes, func(d *config.State) (plan, error) {
		n, err := f(d.AWG)
		if err != nil {
			return plan{}, usagef("%v", err)
		}
		changed := awg.Changed(d.AWG, n)
		if len(changed) == 0 {
			return plan{summary: []string{"No change."}}, nil
		}
		_, warns := awg.Validate(n)
		pl := plan{class: config.Restart}
		for _, k := range changed {
			v := n[k]
			if v == "" {
				v = "(removed)"
			} else if awg.Secret(k) {
				v = "(new secret)"
			}
			pl.summary = append(pl.summary, fmt.Sprintf("awg.%s: %s", k, v))
			if awg.MustMatch(k) {
				pl.reissue = allPeers(d)
			}
		}
		for _, w := range warns {
			pl.summary = append(pl.summary, "WARNING: "+w)
		}
		d.AWG = n
		return pl, nil
	})
}
