package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
)

func (a *App) awgShow(ctx context.Context, c *call) error {
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "# AWG %s\n", st.AWG["version"])
	for _, l := range awg.Lines(st.AWG) {
		v := l.Value
		if awg.Secret(awg.KeyForConf(l.Key)) && !c.showSecrets {
			v = masked
		}
		fmt.Fprintf(a.Out, "%s = %s\n", l.Key, v)
	}
	return nil
}

func (a *App) awgVersion(ctx context.Context, c *call) error {
	return a.awgChange(ctx, c.yes, func(p awg.Params) (awg.Params, error) {
		return awg.Transition(a.Rand, p, c.pos[0])
	})
}

func (a *App) awgSet(ctx context.Context, c *call) error {
	var kv [][2]string
	for _, s := range c.pos {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			return usagef("%q is not key=value", s)
		}
		kv = append(kv, [2]string{k, v})
	}
	return a.awgChange(ctx, c.yes, func(p awg.Params) (awg.Params, error) {
		return awg.Set(p, kv)
	})
}

func (a *App) awgRegenerate(ctx context.Context, c *call) error {
	return a.awgChange(ctx, c.yes, func(p awg.Params) (awg.Params, error) {
		return awg.Regenerate(a.Rand, p, c.pos)
	})
}

// awgChange applies f to the AWG set. A change to a value that must match on
// every peer also needs new peer confs.
func (a *App) awgChange(ctx context.Context, yes bool, f func(awg.Params) (awg.Params, error)) error {
	return a.change(ctx, yes, func(d *config.State) (plan, error) {
		n, err := f(d.AWG)
		if err != nil {
			return plan{}, usagef("%v", err)
		}
		changed := awg.Changed(d.AWG, n)
		if len(changed) == 0 {
			return plan{noop: true, summary: []string{"No change."}}, nil
		}
		_, warns := awg.Validate(n)
		var pl plan
		for _, k := range changed {
			v := n[k]
			if v == "" {
				v = "(removed)"
			} else if awg.Secret(k) {
				v = "(new secret)"
			}
			pl.summary = append(pl.summary, fmt.Sprintf("awg.%s: %s", k, v))
			if awg.MustMatch(k) {
				pl.newConfs(allPeers(d))
			}
		}
		for _, w := range warns {
			pl.summary = append(pl.summary, "WARNING: "+w)
		}
		d.AWG = n
		return pl, nil
	})
}
