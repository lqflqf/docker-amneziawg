package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
)

const masked = "******** (secret; --show-secrets)"

func (a *App) cmdGet(ctx context.Context, c *call) error {
	pos, show := c.pos, c.showSecrets
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
		if s.Secret && !show {
			v = masked
		}
		fmt.Fprintln(a.Out, v)
		return nil
	}
	for _, s := range config.Registry {
		v := st.Get(s.Key)
		note := ""
		if s.Secret && !show {
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
			pl.newConfs(allPeers(d))
		}
		return pl, nil
	})
}

func (a *App) cmdSet(ctx context.Context, c *call) error {
	return a.setOne(ctx, c.pos[0], c.pos[1], c.yes)
}

func (a *App) cmdReset(ctx context.Context, c *call) error {
	s, err := settable(c.pos[0])
	if err != nil {
		return err
	}
	if s.Default == "" {
		return usagef("%s has no built-in default; use awgctl set", s.Key)
	}
	return a.setOne(ctx, s.Key, s.Default, c.yes)
}

// cmdServerRotateKey always needs --yes: a new server key cuts every peer off.
func (a *App) cmdServerRotateKey(ctx context.Context, c *call) error {
	return a.change(ctx, c.yes, func(d *config.State) (plan, error) {
		d.Settings[config.KeyServerPrivateKey] = keys.NewPrivate()
		pub, _ := keys.Public(d.Settings[config.KeyServerPrivateKey])
		return plan{needsYes: true, reissue: allPeersOrServer(d),
			summary: []string{"New server public key: " + pub}}, nil
	})
}

// allPeersOrServer names who needs a new conf after a new server key.
func allPeersOrServer(d *config.State) string {
	if r := allPeers(d); r != "" {
		return r
	}
	return "the server (no peers yet)"
}
