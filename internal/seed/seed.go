// Package seed builds the first configuration from the environment. Env vars
// are read only here, on the first start; later starts ignore them.
package seed

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lqflqf/docker-amneziawg/internal/awg"
	"github.com/lqflqf/docker-amneziawg/internal/config"
	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"github.com/lqflqf/docker-amneziawg/internal/netcheck"
)

// Getenv returns an env var and whether it is set and not empty.
type Getenv func(string) (string, bool)

// Detector finds the public IPv4 address.
type Detector func(context.Context) (string, error)

// Build returns the first state. detect is called only when SERVERURL is not
// set. The state is complete but not yet validated with the real parsers.
func Build(ctx context.Context, r *rand.Rand, getenv Getenv, detect Detector) (*config.State, []string, error) {
	st := &config.State{Settings: map[string]string{}, AWG: awg.Params{}}
	var notes []string
	for _, s := range config.Registry {
		var v string
		if s.Env != "" {
			v, _ = getenv(s.Env)
		}
		switch {
		case s.Key == config.KeyServerPrivateKey:
			st.Settings[s.Key] = keys.NewPrivate()
			continue
		case s.Key == config.KeyEndpointHost && v == "":
			ip, err := detect(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("SERVERURL is not set and the public IPv4 address could not be detected: %w. Set SERVERURL to this server's DNS name or IP", err)
			}
			notes = append(notes, fmt.Sprintf("SERVERURL is not set, detected public IPv4 %s. Set SERVERURL to a DNS name or fixed IP: if the address changes, each peer needs a new conf.", ip))
			v = ip
		case s.Key == config.KeyEndpointHost && strings.EqualFold(v, "auto"):
			return nil, nil, fmt.Errorf("SERVERURL=auto is not supported: leave SERVERURL unset to detect the public IPv4, or set a DNS name or IP")
		case v == "":
			st.Settings[s.Key] = s.Default
			continue
		}
		n, err := s.Check(v)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", s.Env, err)
		}
		st.Settings[s.Key] = n
	}

	version := awg.Default
	if v, ok := getenv("AWG_VERSION"); ok {
		version = strings.TrimSpace(v)
	}
	if !awg.ValidVersion(version) {
		return nil, nil, fmt.Errorf("AWG_VERSION=%q is not one of %s", version, strings.Join(awg.Versions, ", "))
	}
	p := awg.Generate(r, version)

	var pins [][2]string
	for _, k := range awg.Keys {
		if v, ok := getenv(awg.EnvName(k)); ok {
			pins = append(pins, [2]string{k, v})
		}
	}
	// A pinned RandomTrailers changes how S is drawn: draw again first.
	for _, kv := range pins {
		if kv[0] == awg.RandomTrailers {
			sw, ok := awg.NormalizeSwitch(kv[1])
			if !ok {
				return nil, nil, fmt.Errorf("AWG_RANDOM_TRAILERS must be on or off, got %q", kv[1])
			}
			if sw != p[awg.RandomTrailers] {
				p[awg.RandomTrailers] = sw
				awg.Redraw(r, p)
			}
		}
	}
	// With trailers on, one pinned S value is used for all four.
	if p.On(awg.RandomTrailers) && awg.Has2x(version) {
		var sv string
		for _, kv := range pins {
			if strings.HasPrefix(kv[0], "s") && len(kv[0]) == 2 {
				if sv == "" {
					sv = kv[1]
				}
			}
		}
		if sv != "" {
			pins = append([][2]string{{"s", sv}}, pins...)
		}
	}
	p, err := awg.Set(p, pins)
	if err != nil {
		return nil, nil, err
	}
	st.AWG = p
	if errs, _ := awg.Validate(p); len(errs) > 0 {
		var msg []string
		for _, e := range errs {
			msg = append(msg, e.Error())
		}
		return nil, nil, fmt.Errorf("the AWG_* values are not valid: %s", strings.Join(msg, "; "))
	}
	return st, notes, nil
}

// DetectURLs are asked at the same time; the first valid answer in this order wins.
var DetectURLs = []string{"https://ipv4.icanhazip.com", "https://api.ipify.org", "https://ifconfig.me/ip"}

// DetectIPv4 asks the services over HTTPS (IPv4 only, 5 s) and returns the
// answer of the first service in list order that gave a valid IPv4 address.
func DetectIPv4(ctx context.Context, urls []string, client *http.Client) (string, error) {
	if client == nil {
		d := &net.Dialer{Timeout: 3 * time.Second}
		client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return d.DialContext(ctx, "tcp4", addr)
			},
		}}
	}
	results := make([]string, len(urls))
	done := make(chan struct{}, len(urls))
	for i, u := range urls {
		go func(i int, u string) {
			defer func() { done <- struct{}{} }()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return
			}
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			if ip := strings.TrimSpace(string(b)); netcheck.IsIPv4(ip) {
				results[i] = ip
			}
		}(i, u)
	}
	for range urls {
		<-done
	}
	for _, ip := range results {
		if ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no service answered with an IPv4 address")
}

// RetryDelays are the waits before each detection attempt.
var RetryDelays = []time.Duration{0, 5 * time.Second, 15 * time.Second}

// Retrying wraps a detector with RetryDelays.
func Retrying(d Detector, log func(string)) Detector {
	return func(ctx context.Context) (string, error) {
		var err error
		for i, wait := range RetryDelays {
			if wait > 0 {
				log(fmt.Sprintf("public IPv4 detection failed (%v), trying again in %s (attempt %d of %d)", err, wait, i+1, len(RetryDelays)))
				select {
				case <-time.After(wait):
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			var ip string
			if ip, err = d(ctx); err == nil {
				return ip, nil
			}
		}
		return "", err
	}
}
