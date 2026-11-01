package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/lqflqf/docker-amneziawg/internal/apply"
	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/store"
)

func (a *App) cmdPeer(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("usage: awgctl peer add|list|show|export|rename|set|disable|enable|rotate|rm")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("peer "+sub, flag.ContinueOnError)
	yes := fs.Bool("yes", false, "")
	noKeepalive := fs.Bool("no-keepalive", false, "")
	octet := fs.Int("octet", 0, "")
	qr := fs.Bool("qr", false, "")
	asJSON := fs.Bool("json", false, "")
	png := fs.String("png", "", "")
	confOut := fs.String("conf", "", "")
	keepalive := fs.String("keepalive", "", "")
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
		if err := need(1, "add <name> [--no-keepalive] [--octet N] [--qr]"); err != nil {
			return err
		}
		return a.peerAdd(ctx, pos[0], !*noKeepalive, *octet, *qr)
	case "list":
		if err := need(0, "list [--json]"); err != nil {
			return err
		}
		return a.peerList(ctx, *asJSON)
	case "show":
		if err := need(1, "show <peer>"); err != nil {
			return err
		}
		return a.peerShow(ctx, pos[0])
	case "export":
		if err := need(1, "export <peer> [--qr | --png FILE | --conf FILE]"); err != nil {
			return err
		}
		return a.peerExport(ctx, pos[0], *qr, *png, *confOut)
	case "rename":
		if err := need(2, "rename <peer> <new-name>"); err != nil {
			return err
		}
		return a.peerEdit(ctx, pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
			if !config.ValidPeerName(pos[1]) {
				return plan{}, usagef("invalid name %q: use 1-32 of A-Z a-z 0-9 . _ -, starting with a letter or digit", pos[1])
			}
			old := p.Name
			p.Name = pos[1]
			return plan{class: config.Live, summary: []string{fmt.Sprintf("Peer #%d: %s -> %s", p.ID, old, p.Name)}}, nil
		})
	case "set":
		if err := need(1, "set <peer> --keepalive on|off"); err != nil {
			return err
		}
		sw, ok := awg.NormalizeSwitch(*keepalive)
		if !ok {
			return usagef("usage: awgctl peer set <peer> --keepalive on|off")
		}
		return a.peerEdit(ctx, pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
			p.Keepalive = sw == "on"
			return plan{class: config.Live, summary: []string{fmt.Sprintf("Peer #%d %s: keepalive %s", p.ID, p.Name, sw)}}, nil
		})
	case "disable", "enable":
		if err := need(1, sub+" <peer>"); err != nil {
			return err
		}
		return a.peerEdit(ctx, pos[0], false, func(d *config.State, p *store.Peer) (plan, error) {
			p.Enabled = sub == "enable"
			return plan{class: config.Live, summary: []string{fmt.Sprintf("Peer #%d %s: %sd", p.ID, p.Name, sub)}}, nil
		})
	case "rotate":
		if err := need(1, "rotate <peer> --yes"); err != nil {
			return err
		}
		return a.peerEdit(ctx, pos[0], *yes, func(d *config.State, p *store.Peer) (plan, error) {
			p.PrivateKey, p.PublicKey = keys.Pair()
			p.PresharedKey = keys.NewSymmetric()
			return plan{class: config.Live, reissue: fmt.Sprintf("peer #%d %s", p.ID, p.Name),
				summary: []string{fmt.Sprintf("Peer #%d %s: new keys", p.ID, p.Name)}}, nil
		})
	case "rm":
		if err := need(1, "rm <peer> --yes"); err != nil {
			return err
		}
		if !*yes {
			return needYes("peer rm deletes the peer and its keys; run again with --yes")
		}
		return a.change(ctx, true, func(d *config.State) (plan, error) {
			p, err := d.FindPeer(pos[0])
			if err != nil {
				return plan{}, err
			}
			id, name := p.ID, p.Name
			out := d.Peers[:0]
			for _, q := range d.Peers {
				if q.ID != id {
					out = append(out, q)
				}
			}
			d.Peers = out
			return plan{class: config.Live, summary: []string{fmt.Sprintf("Peer #%d %s: deleted", id, name)}}, nil
		})
	}
	return usagef("unknown peer command %q", sub)
}

// peerEdit changes one peer in place.
func (a *App) peerEdit(ctx context.Context, sel string, yes bool, f func(*config.State, *store.Peer) (plan, error)) error {
	return a.change(ctx, yes, func(d *config.State) (plan, error) {
		p, err := d.FindPeer(sel)
		if err != nil {
			return plan{}, err
		}
		return f(d, p)
	})
}

func (a *App) peerAdd(ctx context.Context, name string, keepalive bool, octet int, qr bool) error {
	if !config.ValidPeerName(name) {
		return usagef("invalid name %q: use 1-32 of A-Z a-z 0-9 . _ -, starting with a letter or digit", name)
	}
	if octet != 0 && (octet < 2 || octet > 254) {
		return usagef("--octet must be 2-254")
	}
	err := a.change(ctx, false, func(d *config.State) (plan, error) {
		for _, p := range d.Peers {
			if p.Name == name {
				return plan{}, fmt.Errorf("a peer named %q exists (#%d)", name, p.ID)
			}
			if octet != 0 && p.Octet == octet {
				return plan{}, fmt.Errorf("address .%d is used by peer #%d %s", octet, p.ID, p.Name)
			}
		}
		if len(d.Peers) >= config.MaxPeers {
			return plan{}, fmt.Errorf("the subnet is full (%d peers)", config.MaxPeers)
		}
		o := octet
		if o == 0 {
			var err error
			if o, err = d.FreeOctet(); err != nil {
				return plan{}, err
			}
		}
		priv, pub := keys.Pair()
		d.Peers = append(d.Peers, store.Peer{Name: name, Octet: o, PrivateKey: priv, PublicKey: pub,
			PresharedKey: keys.NewSymmetric(), Keepalive: keepalive, Enabled: true})
		return plan{class: config.Live, summary: []string{fmt.Sprintf("Peer %s: address %s", name, config.PeerAddress(d.Subnet(), o))}}, nil
	})
	if err != nil {
		return err
	}
	st, verr := a.view(ctx)
	if verr != nil {
		return verr
	}
	p, verr := st.FindPeer("name:" + name)
	if verr != nil {
		return verr
	}
	fmt.Fprintf(a.Out, "Peer #%d %s added. Export: awgctl peer export %d --qr\n", p.ID, p.Name, p.ID)
	if qr {
		return a.peerExport(ctx, strconv.FormatInt(p.ID, 10), true, "", "")
	}
	return nil
}

type peerRow struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Address       string `json:"address"`
	PublicKey     string `json:"public_key"`
	Enabled       bool   `json:"enabled"`
	Keepalive     bool   `json:"keepalive"`
	LastHandshake string `json:"last_handshake,omitempty"`
	Received      int64  `json:"rx_bytes"`
	Sent          int64  `json:"tx_bytes"`
	CreatedAt     string `json:"created_at"`
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
		r := peerRow{ID: p.ID, Name: p.Name, Address: config.PeerAddress(st.Subnet(), p.Octet),
			PublicKey: p.PublicKey, Enabled: p.Enabled, Keepalive: p.Keepalive, CreatedAt: p.CreatedAt}
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
		fmt.Fprintln(a.Out, "No peers. Add one with: awgctl peer add <name> --qr")
		return nil
	}
	fmt.Fprintf(a.Out, "%-4s %-20s %-15s %-8s %-9s %-20s %s\n", "ID", "NAME", "ADDRESS", "ENABLED", "KEEPALIVE", "LAST HANDSHAKE", "RX/TX")
	for _, r := range rows {
		hs := "-"
		if r.LastHandshake != "" {
			hs = r.LastHandshake
		}
		fmt.Fprintf(a.Out, "%-4d %-20s %-15s %-8t %-9t %-20s %s/%s\n", r.ID, r.Name, r.Address, r.Enabled, r.Keepalive, hs,
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

func (a *App) peerShow(ctx context.Context, sel string) error {
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	p, err := st.FindPeer(sel)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "id          %d\nname        %s\naddress     %s/32\npublic key  %s\npreshared   %t\nenabled     %t\nkeepalive   %t\ncreated     %s\nupdated     %s\n",
		p.ID, p.Name, config.PeerAddress(st.Subnet(), p.Octet), p.PublicKey, p.PresharedKey != "",
		p.Enabled, p.Keepalive, p.CreatedAt, p.UpdatedAt)
	if x, ok := a.runtimePeers(ctx)[p.PublicKey]; ok && x[0] > 0 {
		fmt.Fprintf(a.Out, "handshake   %s\ntransfer    %s received, %s sent\n",
			time.Unix(x[0], 0).UTC().Format(time.RFC3339), human(x[1]), human(x[2]))
	}
	return nil
}

// peerVisibleRestart lists pending restart keys that change what a peer
// conf must contain, so an export made now works only after the restart.
func peerVisibleRestart(st *config.State, active config.Active) []string {
	var out []string
	for _, k := range config.PendingRestart(st, active) {
		if k == "net.subnet" || k == "server.private_key" {
			out = append(out, k)
		}
		if x, ok := strings.CutPrefix(k, "awg."); ok && awg.MustMatch(x) {
			out = append(out, k)
		}
	}
	return out
}

func (a *App) peerExport(ctx context.Context, sel string, qr bool, png, confOut string) error {
	st, err := a.view(ctx)
	if err != nil {
		return err
	}
	p, err := st.FindPeer(sel)
	if err != nil {
		return err
	}
	f, err := render.Peer(st, *p)
	if err != nil {
		return err
	}
	text := f.String()
	if active, err := (apply.Env{P: a.P, R: a.R}).ReadActive(); err == nil {
		if pend := peerVisibleRestart(st, active); len(pend) > 0 {
			fmt.Fprintf(a.Err, "WARNING: a restart is pending for %s; this conf works after the next container restart.\n", strings.Join(pend, ", "))
		}
	}
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
		fmt.Fprintf(a.Out, "Peer #%d %s (the QR code holds the peer's private key):\n", p.ID, p.Name)
		fmt.Fprint(a.Out, q.ToSmallString(false))
		return nil
	}
	if !wrote {
		fmt.Fprint(a.Out, text)
	}
	return nil
}
