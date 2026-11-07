// Package sys holds the file paths, the command runner and small file helpers.
// Every outside effect goes through here, so tests can use temporary
// directories and a fake runner.
package sys

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
)

// Paths are the locations awgctl reads and writes.
type Paths struct {
	Config string // the /config volume
	Run    string // /run
}

// DefaultPaths are /config and /run, unless AWGCTL_CONFIG_DIR or
// AWGCTL_RUN_DIR say otherwise (tests).
func DefaultPaths() Paths {
	p := Paths{Config: "/config", Run: "/run"}
	if v := os.Getenv("AWGCTL_CONFIG_DIR"); v != "" {
		p.Config = v
	}
	if v := os.Getenv("AWGCTL_RUN_DIR"); v != "" {
		p.Run = v
	}
	return p
}

func (p Paths) DB() string         { return filepath.Join(p.Config, "amneziawg.db") }
func (p Paths) RunDir() string     { return filepath.Join(p.Run, "amneziawg") }
func (p Paths) WG0() string        { return filepath.Join(p.RunDir(), "wg0.conf") }
func (p Paths) Settled() string    { return filepath.Join(p.Run, "amneziawg-settled") }
func (p Paths) DNSDir() string     { return filepath.Join(p.Run, "dnsmasq") }
func (p Paths) DNSConf() string    { return filepath.Join(p.DNSDir(), "dnsmasq.conf") }
func (p Paths) DNSAddress() string { return filepath.Join(p.DNSDir(), "address") }
func (p Paths) DNSState() string   { return filepath.Join(p.Run, "dnsmasq-state") }

// Runner runs a program. The real one is Exec; tests use a fake.
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte, env []string) (stdout, stderr []byte, err error)
}

// Exec runs real programs.
type Exec struct{}

// Run runs name with args; env adds to the current environment.
func (Exec) Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(), env...)
	err := cmd.Run()
	return out.Bytes(), errb.Bytes(), err
}

// WriteAtomic writes data to a new file next to path and renames it over
// path, so a reader never sees a half-written file. It never follows a
// symlink at path: rename replaces the link itself.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rnd := make([]byte, 6)
	rand.Read(rnd)
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(rnd))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
