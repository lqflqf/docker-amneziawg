package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

func (a *App) peerRename(ctx context.Context, c *call) error {
	if len(c.pos) == 2 == c.clear {
		return c.cmd.usageErr()
	}
	if len(c.pos) == 2 && !config.ValidPeerName(c.pos[1]) {
		return usagef("invalid name %q: use %s", c.pos[1], config.NameRule)
	}
	return a.peerEdit(ctx, c.pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
		n := config.DefaultPeerName(p.ID)
		if len(c.pos) == 2 {
			n = c.pos[1]
		}
		if n == p.Name {
			return plan{noop: true, summary: []string{fmt.Sprintf("Peer %d is already named %q.", p.ID, n)}}, nil
		}
		old := p.Name
		p.Name = n
		return plan{summary: []string{fmt.Sprintf("Peer %d: %q -> %q", p.ID, old, p.Name)}}, nil
	})
}

func (a *App) peerSet(ctx context.Context, c *call) error {
	sw, ok := awg.NormalizeSwitch(c.keepalive)
	if !ok {
		return c.cmd.usageErr()
	}
	return a.peerEdit(ctx, c.pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
		if p.Keepalive == (sw == "on") {
			return plan{noop: true, summary: []string{fmt.Sprintf("%s: keepalive is already %s.", label(*p), sw)}}, nil
		}
		p.Keepalive = sw == "on"
		return plan{summary: []string{fmt.Sprintf("%s: keepalive %s", label(*p), sw)}}, nil
	})
}

// peerSetEnabled runs peer enable (on) and peer disable.
func (a *App) peerSetEnabled(ctx context.Context, arg string, on bool) error {
	word := "disabled"
	if on {
		word = "enabled"
	}
	return a.peerEdit(ctx, arg, false, func(d *config.State, p *store.Peer) (plan, error) {
		if p.Enabled == on {
			return plan{noop: true, summary: []string{fmt.Sprintf("%s is already %s.", label(*p), word)}}, nil
		}
		p.Enabled = on
		return plan{summary: []string{fmt.Sprintf("%s: %s", label(*p), word)}}, nil
	})
}

func (a *App) peerRotate(ctx context.Context, c *call) error {
	return a.peerEdit(ctx, c.pos[0], c.yes, func(d *config.State, p *store.Peer) (plan, error) {
		p.PrivateKey, p.PublicKey = keys.Pair()
		p.PresharedKey = keys.NewSymmetric()
		pl := plan{summary: []string{label(*p) + ": new keys"}}
		pl.newConfs(fmt.Sprintf("peer %d", p.ID))
		return pl, nil
	})
}

func (a *App) peerRemove(ctx context.Context, c *call) error {
	id, err := peerID(c.pos[0])
	if err != nil {
		return err
	}
	if !c.yes {
		return needYes("peer rm deletes the peer and its keys; run again with --yes")
	}
	return a.change(ctx, true, func(d *config.State) (plan, error) {
		p, err := d.RemovePeer(id)
		if err != nil {
			return plan{}, err
		}
		return plan{summary: []string{label(p) + ": deleted"}}, nil
	})
}

// peerID parses a peer id argument. Names and "#5" are usage errors.
func peerID(s string) (int, error) {
	id, ok := config.ParseID(s)
	if !ok {
		return 0, usagef("%q is not a peer id: use the number from awgctl peer list", s)
	}
	return id, nil
}

// label names a peer in messages: "Peer 5", or "Peer 5 \"phone\"" for a
// name of its own.
func label(p store.Peer) string {
	if p.Name == config.DefaultPeerName(p.ID) {
		return fmt.Sprintf("Peer %d", p.ID)
	}
	return fmt.Sprintf("Peer %d %q", p.ID, p.Name)
}

// peerEdit changes one peer in place.
func (a *App) peerEdit(ctx context.Context, arg string, yes bool, f func(*config.State, *store.Peer) (plan, error)) error {
	id, err := peerID(arg)
	if err != nil {
		return err
	}
	return a.change(ctx, yes, func(d *config.State) (plan, error) {
		p, err := d.FindPeer(id)
		if err != nil {
			return plan{}, err
		}
		return f(d, p)
	})
}

func (a *App) peerAdd(ctx context.Context, c *call) error {
	name, qr := c.name, c.qr
	if name != "" && !config.ValidPeerName(name) {
		return usagef("invalid name %q: use %s", name, config.NameRule)
	}
	var added store.Peer
	var code string
	err := a.change(ctx, false, func(d *config.State) (plan, error) {
		ps, err := d.AddPeers(1, name, !c.noKeepalive)
		if err != nil {
			return plan{}, err
		}
		added = ps[0]
		// Render the QR code before the commit: after it, nothing may fail.
		if qr {
			if code, err = qrText(d, added); err != nil {
				return plan{}, err
			}
		}
		return plan{summary: []string{addedLine(d, added)}}, nil
	})
	if err != nil {
		return err
	}
	if qr {
		fmt.Fprintf(a.Out, "%s (the QR code holds the peer's private key; it works after the restart):\n", label(added))
		fmt.Fprint(a.Out, code)
		return nil
	}
	fmt.Fprintf(a.Out, "Export: awgctl peer export %d --qr\n", added.ID)
	return nil
}

func (a *App) peerAddMany(ctx context.Context, c *call) error {
	n, err := strconv.Atoi(c.pos[0])
	if err != nil || n < 1 || n > config.MaxPeers {
		return usagef("the count must be a number 1-%d", config.MaxPeers)
	}
	return a.change(ctx, false, func(d *config.State) (plan, error) {
		ps, err := d.AddPeers(n, "", !c.noKeepalive)
		if err != nil {
			return plan{}, err
		}
		var pl plan
		for _, p := range ps {
			pl.summary = append(pl.summary, addedLine(d, p))
		}
		pl.summary = append(pl.summary, fmt.Sprintf("%d peer(s) added. Export each one: awgctl peer export <id> --qr", len(ps)))
		return pl, nil
	})
}

func addedLine(d *config.State, p store.Peer) string {
	return fmt.Sprintf("%s added: address %s", label(p), config.PeerAddress(d.Subnet(), p.ID))
}

// qrText renders the conf of p as a QR code for the terminal.
func qrText(st *config.State, p store.Peer) (string, error) {
	f, err := render.Peer(st, p)
	if err != nil {
		return "", err
	}
	return qrSmall(f.String())
}

// qrSmall renders text as a QR code for the terminal.
func qrSmall(text string) (string, error) {
	q, err := qrcode.New(text, qrcode.Low)
	if err != nil {
		return "", err
	}
	return q.ToSmallString(false), nil
}

type peerRow struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Address       string `json:"address"`
	PublicKey     string `json:"public_key"`
	Enabled       bool   `json:"enabled"`
	Keepalive     bool   `json:"keepalive"`
	LastHandshake string `json:"last_handshake,omitempty"`
	Received      int64  `json:"rx_bytes"`
	Sent          int64  `json:"tx_bytes"`
	handshake     int64  // Unix time of the last handshake (0 = none)
}

// peerStats are the counters of one peer on the running wg0.
type peerStats struct {
	handshake int64 // Unix time of the last handshake (0 = none)
	rx, tx    int64 // bytes received and sent
}

// lastHandshake returns the handshake time in RFC 3339, or "" for none.
func (s peerStats) lastHandshake() string {
	if s.handshake <= 0 {
		return ""
	}
	return time.Unix(s.handshake, 0).UTC().Format(time.RFC3339)
}

// runtimePeers reads handshakes and transfer counters from wg0 (empty when down).
func (a *App) runtimePeers(ctx context.Context) map[string]peerStats {
	out := map[string]peerStats{}
	b, _, err := a.R.Run(ctx, "awg", []string{"show", render.Interface, "dump"}, nil, nil)
	if err != nil {
		return out
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for _, l := range lines[min(1, len(lines)):] {
		f := strings.Split(l, "\t")
		if len(f) < 7 {
			continue
		}
		var s peerStats
		s.handshake, _ = strconv.ParseInt(f[4], 10, 64)
		s.rx, _ = strconv.ParseInt(f[5], 10, 64)
		s.tx, _ = strconv.ParseInt(f[6], 10, 64)
		out[f[0]] = s
	}
	return out
}

func (a *App) peerList(ctx context.Context, c *call) error {
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	rt := a.runtimePeers(ctx)
	var rows []peerRow
	for _, p := range st.Peers {
		r := peerRow{ID: p.ID, Name: p.Name, Address: config.PeerAddress(st.Subnet(), p.ID),
			PublicKey: p.PublicKey, Enabled: p.Enabled, Keepalive: p.Keepalive}
		if x, ok := rt[p.PublicKey]; ok {
			r.LastHandshake, r.Received, r.Sent, r.handshake = x.lastHandshake(), x.rx, x.tx, x.handshake
		}
		rows = append(rows, r)
	}
	if c.json {
		enc := json.NewEncoder(a.Out)
		enc.SetIndent("", "  ")
		if rows == nil {
			rows = []peerRow{}
		}
		return enc.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "No peers. Add one with: awgctl peer add --qr")
		return nil
	}
	fmt.Fprintf(a.Out, "%-4s %-32s %-15s %-8s %-9s %-20s %s\n", "ID", "NAME", "ADDRESS", "ENABLED", "KEEPALIVE", "LAST HANDSHAKE", "RX/TX")
	now := a.now()
	for _, r := range rows {
		hs := "-"
		if r.handshake > 0 {
			hs = ago(now.Sub(time.Unix(r.handshake, 0)))
		}
		fmt.Fprintf(a.Out, "%-4d %-32s %-15s %-8t %-9t %-20s %s/%s\n", r.ID, r.Name, r.Address, r.Enabled, r.Keepalive, hs,
			human(r.Received), human(r.Sent))
	}
	return nil
}

// ago returns a short relative time, for example "3m 12s ago". A negative
// duration (clock skew) shows as "0s ago".
func ago(d time.Duration) string {
	s := max(int64(d/time.Second), 0)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds ago", s)
	case s < 3600:
		return fmt.Sprintf("%dm %ds ago", s/60, s%60)
	case s < 86400:
		return fmt.Sprintf("%dh %dm ago", s/3600, s%3600/60)
	default:
		return fmt.Sprintf("%dd %dh ago", s/86400, s%86400/3600)
	}
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (a *App) peerShow(ctx context.Context, c *call) error {
	id, err := peerID(c.pos[0])
	if err != nil {
		return err
	}
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	p, err := st.FindPeer(id)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "id          %d\nname        %s\naddress     %s/32\npublic key  %s\npreshared   %t\nenabled     %t\nkeepalive   %t\n",
		p.ID, p.Name, config.PeerAddress(st.Subnet(), p.ID), p.PublicKey, p.PresharedKey != "",
		p.Enabled, p.Keepalive)
	if x, ok := a.runtimePeers(ctx)[p.PublicKey]; ok && x.lastHandshake() != "" {
		fmt.Fprintf(a.Out, "handshake   %s\ntransfer    %s received, %s sent\n",
			x.lastHandshake(), human(x.rx), human(x.tx))
	}
	return nil
}

func (a *App) peerExport(ctx context.Context, c *call) error {
	qr, png, confOut := c.qr, c.png, c.conf
	id, err := peerID(c.pos[0])
	if err != nil {
		return err
	}
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	p, err := st.FindPeer(id)
	if err != nil {
		return err
	}
	f, err := render.Peer(st, *p)
	if err != nil {
		return err
	}
	text := f.String()
	wrote := false
	if confOut != "" {
		if err := os.WriteFile(confOut, []byte(text), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(a.Err, "Wrote %s\n", confOut)
		wrote = true
	}
	if png != "" {
		b, err := qrcode.Encode(text, qrcode.Low, 512)
		if err != nil {
			return err
		}
		if err := os.WriteFile(png, b, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(a.Err, "Wrote %s\n", png)
		wrote = true
	}
	if qr {
		code, err := qrSmall(text)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "%s (the QR code holds the peer's private key):\n", label(*p))
		fmt.Fprint(a.Out, code)
		return nil
	}
	if !wrote {
		fmt.Fprint(a.Out, text)
	}
	return nil
}
