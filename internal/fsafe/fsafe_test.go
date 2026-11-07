package fsafe

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func me() Owner { return Owner{os.Getuid(), os.Getgid()} }

func TestCheckRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "amneziawg.db")
	if err := CheckConfig(dir, db, false, me()); err != nil {
		t.Fatalf("empty volume: %v", err)
	}
	os.WriteFile(db, nil, 0o600)
	if os.Getuid() == 0 {
		// A volume of an earlier PUID (4242) under a new one (1000).
		os.Chown(db, 4242, 4242)
		now := Owner{UID: 1000, GID: 1000}
		if err := CheckConfig(dir, db, false, now); err == nil {
			t.Fatal("a DB of another owner passed the strict check")
		}
		if err := CheckConfig(dir, db, true, now); err != nil {
			t.Fatalf("init must accept a volume of an earlier PUID: %v", err)
		}
	}
	os.Remove(db)
	os.Symlink("/etc/shadow", db)
	if err := CheckConfig(dir, db, true, me()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked DB: %v", err)
	}
	os.Remove(db)
	os.Symlink("/etc/shadow", db+"-journal")
	if err := CheckConfig(dir, db, true, me()); err == nil {
		t.Fatal("symlinked journal accepted")
	}
	os.Remove(db + "-journal")
	os.Mkdir(db, 0o700)
	if err := CheckConfig(dir, db, false, me()); err == nil {
		t.Fatal("a directory as the DB accepted")
	}
}

func TestChownTreeAndHardenSkipLinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target")
	os.WriteFile(outside, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "amneziawg.db"), nil, 0o644)
	os.MkdirAll(filepath.Join(dir, "legacy-x"), 0o755)
	os.WriteFile(filepath.Join(dir, "legacy-x", "a.conf"), nil, 0o644)
	os.Symlink(outside, filepath.Join(dir, "legacy-x", "link"))
	if err := Harden(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "amneziawg.db")); fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "legacy-x")); fi.Mode().Perm() != 0o700 {
		t.Errorf("legacy-x mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(outside); fi.Mode().Perm() != 0o644 {
		t.Errorf("Harden followed a symlink: %v", fi.Mode())
	}
	if err := ChownTree(dir, me()); err != nil {
		t.Fatal(err)
	}
}

func TestLookupOwnerWithoutAbc(t *testing.T) {
	if _, err := user.Lookup("abc"); err == nil {
		t.Skip("this host has an abc user")
	}
	if _, err := LookupOwner(); err == nil {
		t.Fatal("LookupOwner found a missing abc user")
	}
}

type noStat struct{ fs.FileInfo }

func (noStat) Sys() any { return nil }

func TestOwnerWithoutStat(t *testing.T) {
	if uid, gid := owner(noStat{}); uid != -1 || gid != -1 {
		t.Fatalf("got %d:%d, want -1:-1", uid, gid)
	}
}

func TestCheckPathKinds(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o600)
	for name, c := range map[string]struct {
		path    string
		wantDir bool
		want    string
	}{
		"file as a dir":   {file, true, "is not a directory"},
		"dir as a file":   {dir, false, "is not a regular file"},
		"under a file":    {filepath.Join(file, "x"), false, "not a directory"},
		"missing is fine": {filepath.Join(dir, "missing"), false, ""},
		"good file":       {file, false, ""},
		"good dir":        {dir, true, ""},
	} {
		err := CheckPath(c.path, c.wantDir, false, me())
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestChownTreeMissingRoot(t *testing.T) {
	if err := ChownTree(filepath.Join(t.TempDir(), "missing"), me()); !os.IsNotExist(err) {
		t.Fatalf("got %v, want a not-exist error", err)
	}
}

func TestChownTreeToOtherOwner(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "amneziawg.db")
	os.WriteFile(file, nil, 0o600)
	outside := filepath.Join(t.TempDir(), "target")
	os.WriteFile(outside, nil, 0o600)
	os.Symlink(outside, filepath.Join(dir, "link"))
	other := Owner{UID: 4242, GID: 4243}
	err := ChownTree(dir, other)
	if os.Geteuid() != 0 {
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("non-root chown to another uid: got %v, want a permission error", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, file} {
		fi, _ := os.Lstat(p)
		if uid, gid := owner(fi); uid != other.UID || gid != other.GID {
			t.Errorf("%s: owner %d:%d", p, uid, gid)
		}
	}
	if fi, _ := os.Lstat(outside); fi.Sys().(*syscall.Stat_t).Uid == uint32(other.UID) {
		t.Error("ChownTree followed a symlink")
	}
}

// As root, the test runs ChownTree and Harden with another effective uid.
func TestFirstErrorWithoutPrivilege(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to change the effective uid")
	}
	dir := t.TempDir()
	os.Chmod(filepath.Dir(dir), 0o755)
	os.Chmod(dir, 0o755)
	os.Mkdir(filepath.Join(dir, "legacy-x"), 0o755)
	os.WriteFile(filepath.Join(dir, "amneziawg.db"), nil, 0o644)
	if err := syscall.Setresuid(-1, 4242, -1); err != nil {
		t.Skipf("cannot change the effective uid: %v", err)
	}
	chownErr := ChownTree(dir, Owner{UID: 4242, GID: 4242})
	hardenErr := Harden(dir)
	if err := syscall.Setresuid(-1, 0, -1); err != nil {
		panic(err)
	}
	if !errors.Is(chownErr, os.ErrPermission) {
		t.Errorf("ChownTree: got %v, want a permission error", chownErr)
	}
	if !errors.Is(hardenErr, os.ErrPermission) {
		t.Errorf("Harden: got %v, want a permission error", hardenErr)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "amneziawg.db")); fi.Mode().Perm() != 0o644 {
		t.Errorf("db mode changed to %v", fi.Mode())
	}
}
