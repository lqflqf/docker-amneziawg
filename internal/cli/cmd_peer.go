package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
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

func (a *App) cmdPeer(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("usage: awgctl peer add|add-many|list|show|export|rename|set|disable|enable|rotate|rm")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("peer "+sub, flag.ContinueOnError)
	yes := fs.Bool("yes", false, "")
	name := fs.String("name", "", "")
	noKeepalive := fs.Bool("no-keepalive", false, "")
	qr := fs.Bool("qr", false, "")
	asJSON := fs.Bool("json", false, "")
	png := fs.String("png", "", "")
	confOut := fs.String("conf", "", "")
	keepalive := fs.String("keepalive", "", "")
	clearName := fs.Bool("clear", false, "")
	pos, err := parse(fs, rest)
	if err != nil {
		return err
	}
	need := func(n int, u string) error {
		if len(pos) != n {
			return usagef("usage: awgctl peer %s", u)
		}
		return nil
	}
	switch sub {
	case "add":
		if err := need(0, "add [--name NAME] [--no-keepalive] [--qr]"); err != nil {
			return err
		}
		return a.peerAdd(ctx, *name, !*noKeepalive, *qr)
	case "add-many":
		if err := need(1, "add-many <count> [--no-keepalive]"); err != nil {
			return err
		}
		n, err := strconv.Atoi(pos[0])
		if err != nil || n < 1 || n > config.MaxPeers {
			return usagef("the count must be a number 1-%d", config.MaxPeers)
		}
		return a.peerAddMany(ctx, n, !*noKeepalive)
	case "list":
		if err := need(0, "list [--json]"); err != nil {
			return err
		}
		return a.peerList(ctx, *asJSON)
	case "show":
		if err := need(1, "show <id>"); err != nil {
			return err
		}
		return a.peerShow(ctx, pos[0])
	case "export":
		if err := need(1, "export <id> [--qr | --png FILE | --conf FILE]"); err != nil {
			return err
		}
		return a.peerExport(ctx, pos[0], *qr, *png, *confOut)
	case "rename":
		if !(len(pos) == 2 && !*clearName || len(pos) == 1 && *clearName) {
			return usagef("usage: awgctl peer rename <id> <new-name> | peer rename <id> --clear")
		}
		if len(pos) == 2 && !config.ValidPeerName(pos[1]) {
			return usagef("invalid name %q: use %s", pos[1], config.NameRule)
		}
		return a.peerEdit(ctx, pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
			n := config.DefaultPeerName(p.ID)
			if len(pos) == 2 {
				n = pos[1]
			}
			if n == p.Name {
				return plan{noop: true, summary: []string{fmt.Sprintf("Peer %d is already named %q.", p.ID, n)}}, nil
			}
			old := p.Name
			p.Name = n
			return plan{summary: []string{fmt.Sprintf("Peer %d: %q -> %q", p.ID, old, p.Name)}}, nil
		})
	case "set":
		if err := need(1, "set <id> --keepalive on|off"); err != nil {
			return err
		}
		sw, ok := awg.NormalizeSwitch(*keepalive)
		if !ok {
			return usagef("usage: awgctl peer set <id> --keepalive on|off")
		}
		return a.peerEdit(ctx, pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
			if p.Keepalive == (sw == "on") {
				return plan{noop: true, summary: []string{fmt.Sprintf("%s: keepalive is already %s.", label(*p), sw)}}, nil
			}
			p.Keepalive = sw == "on"
			return plan{summary: []string{fmt.Sprintf("%s: keepalive %s", label(*p), sw)}}, nil
		})
	case "disable", "enable":
		if err := need(1, sub+" <id>"); err != nil {
			return err
		}
		return a.peerEdit(ctx, pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
			if p.Enabled == (sub == "enable") {
				return plan{noop: true, summary: []string{fmt.Sprintf("%s is already %sd.", label(*p), sub)}}, nil
			}
			p.Enabled = sub == "enable"
			return plan{summary: []string{fmt.Sprintf("%s: %sd", label(*p), sub)}}, nil
		})
	case "rotate":
		if err := need(1, "rotate <id> --yes"); err != nil {
			return err
		}
		return a.peerEdit(ctx, pos[0], *yes, func(d *config.State, p *store.Peer) (plan, error) {
			p.PrivateKey, p.PublicKey = keys.Pair()
			p.PresharedKey = keys.NewSymmetric()
			return plan{reissue: fmt.Sprintf("peer %d", p.ID),
				summary: []string{label(*p) + ": new keys"}}, nil
		})
	case "rm":
		if err := need(1, "rm <id> --yes"); err != nil {
			return err
		}
		id, err := peerID(pos[0])
		if err != nil {
			return err
		}
		if !*yes {
			return needYes("peer rm deletes the peer and its keys; run again with --yes")
		}
		return a.change(ctx, true, func(d *config.State) (plan, error) {
			p, err := d.FindPeer(id)
			if err != nil {
				return plan{}, err
			}
			l := label(*p)
			out := d.Peers[:0]
			for _, q := range d.Peers {
				if q.ID != id {
					out = append(out, q)
				}
			}
			d.Peers = out
			return plan{summary: []string{l + ": deleted"}}, nil
		})
	}
	return usagef("unknown peer command %q", sub)
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

// newPeers adds n peers with new keys at the lowest free ids. An empty name
// gives each peer its default name "peer <id>".
func newPeers(d *config.State, n int, name string, keepalive bool) ([]store.Peer, error) {
	ids, err := d.FreeIDs(n)
	if err != nil {
		return nil, err
	}
	names := map[string]int{}
	for _, p := range d.Peers {
		names[p.Name] = p.ID
	}
	var out []store.Peer
	for _, id := range ids {
		nm := name
		if nm == "" {
			nm = config.DefaultPeerName(id)
		}
		if other, ok := names[nm]; ok {
			return nil, fmt.Errorf("the name %q is used by peer %d", nm, other)
		}
		priv, pub := keys.Pair()
		p := store.Peer{ID: id, Name: nm, PrivateKey: priv, PublicKey: pub,
			PresharedKey: keys.NewSymmetric(), Keepalive: keepalive, Enabled: true}
		d.Peers = append(d.Peers, p)
		out = append(out, p)
	}
	sort.Slice(d.Peers, func(i, j int) bool { return d.Peers[i].ID < d.Peers[j].ID })
	return out, nil
}

func (a *App) peerAdd(ctx context.Context, name string, keepalive, qr bool) error {
	if name != "" && !config.ValidPeerName(name) {
		return usagef("invalid name %q: use %s", name, config.NameRule)
	}
	var added store.Peer
	var code string
	err := a.change(ctx, false, func(d *config.State) (plan, error) {
		ps, err := newPeers(d, 1, name, keepalive)
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
		return plan{summary: []string{fmt.Sprintf("%s added: address %s", label(added), config.PeerAddress(d.Subnet(), added.ID))}}, nil
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

func (a *App) peerAddMany(ctx context.Context, n int, keepalive bool) error {
	return a.change(ctx, false, func(d *config.State) (plan, error) {
		ps, err := newPeers(d, n, "", keepalive)
		if err != nil {
			return plan{}, err
		}
		var pl plan
		for _, p := range ps {
			pl.summary = append(pl.summary, fmt.Sprintf("%s added: address %s", label(p), config.PeerAddress(d.Subnet(), p.ID)))
		}
		pl.summary = append(pl.summary, fmt.Sprintf("%d peer(s) added. Export each one: awgctl peer export <id> --qr", len(ps)))
		return pl, nil
	})
}

// qrText renders the conf of p as a QR code for the terminal.
func qrText(st *config.State, p store.Peer) (string, error) {
	f, err := render.Peer(st, p)
	if err != nil {
		return "", err
	}
	q, err := qrcode.New(f.String(), qrcode.Low)
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
}

// runtimePeers reads handshakes and transfer counters from wg0 (empty when down).
func (a *App) runtimePeers(ctx context.Context) map[string][3]int64 {
	out := map[string][3]int64{}
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
		hs, _ := strconv.ParseInt(f[4], 10, 64)
		rx, _ := strconv.ParseInt(f[5], 10, 64)
		tx, _ := strconv.ParseInt(f[6], 10, 64)
		out[f[0]] = [3]int64{hs, rx, tx}
	}
	return out
}

func (a *App) peerList(ctx context.Context, asJSON bool) error {
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
			if x[0] > 0 {
				r.LastHandshake = time.Unix(x[0], 0).UTC().Format(time.RFC3339)
			}
			r.Received, r.Sent = x[1], x[2]
		}
		rows = append(rows, r)
	}
	if asJSON {
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
	for _, r := range rows {
		hs := "-"
		if r.LastHandshake != "" {
			hs = r.LastHandshake
		}
		fmt.Fprintf(a.Out, "%-4d %-32s %-15s %-8t %-9t %-20s %s/%s\n", r.ID, r.Name, r.Address, r.Enabled, r.Keepalive, hs,
			human(r.Received), human(r.Sent))
	}
	return nil
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

func (a *App) peerShow(ctx context.Context, arg string) error {
	id, err := peerID(arg)
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
	if x, ok := a.runtimePeers(ctx)[p.PublicKey]; ok && x[0] > 0 {
		fmt.Fprintf(a.Out, "handshake   %s\ntransfer    %s received, %s sent\n",
			time.Unix(x[0], 0).UTC().Format(time.RFC3339), human(x[1]), human(x[2]))
	}
	return nil
}

func (a *App) peerExport(ctx context.Context, arg string, qr bool, png, confOut string) error {
	id, err := peerID(arg)
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
		q, err := qrcode.New(text, qrcode.Low)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.Out, "%s (the QR code holds the peer's private key):\n", label(*p))
		fmt.Fprint(a.Out, q.ToSmallString(false))
		return nil
	}
	if !wrote {
		fmt.Fprint(a.Out, text)
	}
	return nil
}
