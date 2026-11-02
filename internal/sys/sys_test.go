package sys_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/sys"
)

func TestDefaultPaths(t *testing.T) {
	t.Setenv("AWGCTL_CONFIG_DIR", "")
	t.Setenv("AWGCTL_RUN_DIR", "")
	if p := sys.DefaultPaths(); p.Config != "/config" || p.Run != "/run" {
		t.Errorf("defaults = %+v", p)
	}
	t.Setenv("AWGCTL_CONFIG_DIR", "/c")
	t.Setenv("AWGCTL_RUN_DIR", "/r")
	p := sys.DefaultPaths()
	for _, c := range [][2]string{
		{p.DB(), "/c/amneziawg.db"},
		{p.Backups(), "/c/backups"},
		{p.RunDir(), "/r/amneziawg"},
		{p.WG0(), "/r/amneziawg/wg0.conf"},
		{p.ActiveJSON(), "/r/amneziawg/active.json"},
		{p.StateJSON(), "/r/amneziawg/state.json"},
		{p.Lock(), "/r/awgctl.lock"},
		{p.Settled(), "/r/amneziawg-settled"},
		{p.DNSDir(), "/r/dnsmasq"},
		{p.DNSConf(), "/r/dnsmasq/dnsmasq.conf"},
		{p.DNSAddress(), "/r/dnsmasq/address"},
		{p.DNSState(), "/r/dnsmasq-state"},
		{p.S6DNS(), "/r/service/svc-dnsmasq"},
	} {
		if c[0] != c[1] {
			t.Errorf("got %s, want %s", c[0], c[1])
		}
	}
}

func TestExecRun(t *testing.T) {
	ctx := context.Background()
	out, errb, err := sys.Exec{}.Run(ctx, "sh", []string{"-c", `cat; echo "$AWGCTL_TEST_VAR"; echo oops >&2`},
		[]byte("in\n"), []string{"AWGCTL_TEST_VAR=val"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "in\nval\n" || string(errb) != "oops\n" {
		t.Errorf("stdout %q, stderr %q", out, errb)
	}

	_, errb, err = sys.Exec{}.Run(ctx, "sh", []string{"-c", "echo bad >&2; exit 3"}, nil, nil)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 3 || string(errb) != "bad\n" {
		t.Errorf("err %v, stderr %q", err, errb)
	}

	if _, _, err := (sys.Exec{}).Run(ctx, "awgctl-no-such-program", nil, nil, nil); err == nil {
		t.Error("a missing program must fail")
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "file")
	if err := sys.WriteAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sys.WriteAtomic(path, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	fi, _ := os.Stat(path)
	if string(b) != "two" || fi.Mode().Perm() != 0o644 {
		t.Errorf("content %q, mode %v", b, fi.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("a temporary file stays: %v", entries)
	}
}

func TestWriteAtomicReplacesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := sys.WriteAtomic(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("the link target changed to %q", b)
	}
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("the link is not replaced: %v %v", fi, err)
	}
}

func TestWriteAtomicErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sys.WriteAtomic(filepath.Join(file, "sub", "x"), []byte("x"), 0o600); err == nil {
		t.Error("a file as parent directory must fail")
	}

	// Rename of a file over a directory fails; the temporary file must go.
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, "isdir", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sys.WriteAtomic(filepath.Join(sub, "isdir"), []byte("x"), 0o600); err == nil {
		t.Error("a directory at the path must fail")
	}
	entries, _ := os.ReadDir(sub)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("a temporary file stays: %s", e.Name())
		}
	}
}
