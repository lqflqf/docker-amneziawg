package fsafe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func me() Owner { return Owner{os.Getuid(), os.Getgid()} }

func TestCheckRefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "amneziawg.db")
	if err := CheckConfig(dir, db, filepath.Join(dir, "backups"), me()); err != nil {
		t.Fatalf("empty volume: %v", err)
	}
	os.Symlink("/etc/shadow", db)
	if err := CheckConfig(dir, db, filepath.Join(dir, "backups"), me()); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked DB: %v", err)
	}
	os.Remove(db)
	os.Symlink("/etc", filepath.Join(dir, "backups"))
	if err := CheckConfig(dir, db, filepath.Join(dir, "backups"), me()); err == nil {
		t.Fatal("symlinked backups/ accepted")
	}
	os.Remove(filepath.Join(dir, "backups"))
	os.Mkdir(db, 0o700)
	if err := CheckConfig(dir, db, filepath.Join(dir, "backups"), me()); err == nil {
		t.Fatal("a directory as the DB accepted")
	}
}

func TestChownTreeAndHardenSkipLinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target")
	os.WriteFile(outside, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "amneziawg.db"), nil, 0o644)
	os.MkdirAll(filepath.Join(dir, "backups"), 0o755)
	os.WriteFile(filepath.Join(dir, "backups", "a.db"), nil, 0o644)
	os.Symlink(outside, filepath.Join(dir, "backups", "link"))
	if err := Harden(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "amneziawg.db")); fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Join(dir, "backups")); fi.Mode().Perm() != 0o700 {
		t.Errorf("backups mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(outside); fi.Mode().Perm() != 0o644 {
		t.Errorf("Harden followed a symlink: %v", fi.Mode())
	}
	if err := ChownTree(dir, me()); err != nil {
		t.Fatal(err)
	}
}
