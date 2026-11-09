// Package check tests a configuration before it is saved or used: every rule,
// then the server conf, one peer conf and the dnsmasq conf with the real
// parsers (awg and dnsmasq).
package check

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/render"
	"github.com/lqflqf/docker-amneziawg/internal/store"
	"github.com/lqflqf/docker-amneziawg/internal/sys"
)

// Env is what the checks need from the outside.
type Env struct {
	R sys.Runner
}

func (e Env) run(ctx context.Context, name string, args ...string) (string, error) {
	out, errb, err := e.R.Run(ctx, name, args, nil, nil)
	if err != nil {
		msg := strings.TrimSpace(string(errb) + " " + string(out))
		return string(out), fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, msg)
	}
	return string(out), nil
}

const parseCheckIface = "awgparsecheck"

var endpointRe = regexp.MustCompile(`(?m)^(\s*Endpoint\s*=).*$`)

// ParseCheck parses a conf with awg itself, as tunnel start would. awg-quick
// strip drops the awg-quick keys; awg setconf parses the rest and only then
// tries to apply it to an interface that does not exist, which fails with
// "Unable to modify interface". Any other failure is a parse error.
func (e Env) ParseCheck(ctx context.Context, text, what string) error {
	if _, err := net.InterfaceByName(parseCheckIface); err == nil {
		return fmt.Errorf("an interface named %s exists; remove it so that confs can be checked", parseCheckIface)
	}
	dir, err := os.MkdirTemp("", "awgcheck")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	conf := filepath.Join(dir, parseCheckIface+".conf")
	if err := os.WriteFile(conf, []byte(text), 0o600); err != nil {
		return err
	}
	stripped, errb, err := e.R.Run(ctx, "awg-quick", []string{"strip", conf}, nil, nil)
	if err != nil {
		return fmt.Errorf("%s: awg-quick strip: %s", what, strings.TrimSpace(string(errb)))
	}
	// setconf resolves host names; whether the endpoint resolves now is not
	// the question.
	stripped = endpointRe.ReplaceAll(stripped, []byte("$1 127.0.0.1:51820"))
	sf := filepath.Join(dir, "stripped")
	if err := os.WriteFile(sf, stripped, 0o600); err != nil {
		return err
	}
	out, errb, _ := e.R.Run(ctx, "awg", []string{"setconf", parseCheckIface, sf}, nil, []string{"WG_ENDPOINT_RESOLUTION_RETRIES=0"})
	msg := string(out) + string(errb)
	if strings.Contains(msg, "Unable to modify interface") {
		return nil
	}
	return fmt.Errorf("%s is rejected by awg: %s", what, strings.Join(strings.Fields(msg), " "))
}

// DnsmasqTest checks a dnsmasq conf with dnsmasq --test.
func (e Env) DnsmasqTest(ctx context.Context, text string) error {
	f, err := os.CreateTemp("", "dnsmasq-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(f.Name(), 0o644)
	}
	if err != nil {
		return err
	}
	if _, err := e.run(ctx, "dnsmasq", "--test", "-C", f.Name()); err != nil {
		return fmt.Errorf("dnsmasq rejects the config: %w", err)
	}
	return nil
}

// Check validates a configuration completely: every rule, then the server
// conf, one peer conf and the dnsmasq conf with the real parsers.
func (e Env) Check(ctx context.Context, d *config.State) error {
	if errs, _ := d.Validate(); len(errs) > 0 {
		return errors.Join(errs...)
	}
	srv, err := render.Server(d)
	if err != nil {
		return err
	}
	if err := e.ParseCheck(ctx, srv.String(), "the server conf"); err != nil {
		return err
	}
	// Peer confs differ only in keys and address; one shows that the shape
	// parses. With no peers, check a throwaway one.
	sample := store.Peer{ID: config.MaxPeerID, Name: "check", Enabled: true}
	if len(d.Peers) > 0 {
		sample = d.Peers[0]
	} else {
		sample.PrivateKey, sample.PublicKey = keys.Pair()
		sample.PresharedKey = keys.NewSymmetric()
	}
	pc, err := render.Peer(d, sample)
	if err != nil {
		return err
	}
	if err := e.ParseCheck(ctx, pc.String(), "the peer conf"); err != nil {
		return err
	}
	return e.DnsmasqTest(ctx, render.Dnsmasq(d.Get(config.KeyDNSUpstream)))
}
