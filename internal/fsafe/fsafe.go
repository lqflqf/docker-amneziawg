// Package fsafe guards the /config volume: a user who can write to the volume
// must not be able to make root follow a symlink out of it.
package fsafe

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Owner is the uid:gid that owns /config (the abc user, PUID:PGID).
type Owner struct{ UID, GID int }

// LookupOwner returns the uid and gid of the abc user.
func LookupOwner() (Owner, error) {
	u, err := user.Lookup("abc")
	if err != nil {
		return Owner{}, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return Owner{uid, gid}, nil
}

func owner(fi fs.FileInfo) (int, int) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}

// CheckPath refuses a path that is a symlink, or that is not of the expected
// kind, or (unless anyOwner) that is owned by someone other than root or the
// volume owner. A missing path is fine.
func CheckPath(path string, wantDir, anyOwner bool, o Owner) error {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to follow it", path)
	}
	if wantDir && !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	if !wantDir && !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	uid, _ := owner(fi)
	if !anyOwner && uid != 0 && uid != o.UID {
		return fmt.Errorf("%s is owned by uid %d, expected root or %d (PUID)", path, uid, o.UID)
	}
	return nil
}

// CheckConfig checks /config itself, the DB, its journal and backups/. With
// anyOwner (awgctl init, which then gives /config to PUID:PGID), a volume of
// an earlier PUID is accepted; symlinks and wrong file kinds never are.
func CheckConfig(configDir, db, backups string, anyOwner bool, o Owner) error {
	for _, c := range []struct {
		path string
		dir  bool
	}{{configDir, true}, {db, false}, {db + "-journal", false}, {backups, true}} {
		if err := CheckPath(c.path, c.dir, anyOwner, o); err != nil {
			return err
		}
	}
	return nil
}

// Harden sets the modes of what awgctl owns in /config: the DB 600, backups
// and legacy-* directories 700 with files 600. Symlinks are skipped.
func Harden(configDir string) error {
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if fi, err := os.Lstat(filepath.Join(configDir, "amneziawg.db")); err == nil && fi.Mode().IsRegular() {
		keep(os.Chmod(filepath.Join(configDir, "amneziawg.db"), 0o600))
	}
	entries, _ := os.ReadDir(configDir)
	for _, e := range entries {
		if !e.IsDir() || !(e.Name() == "backups" || strings.HasPrefix(e.Name(), "legacy-")) {
			continue
		}
		dir := filepath.Join(configDir, e.Name())
		keep(filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if d.IsDir() {
				keep(os.Chmod(p, 0o700))
			} else if d.Type().IsRegular() {
				keep(os.Chmod(p, 0o600))
			}
			return nil
		}))
	}
	return firstErr
}

// ChownTree gives everything under root to o. It never follows a symlink
// (WalkDir does not descend into links; Lchown changes the link itself, which
// is skipped anyway) and touches only what is not owned by o already.
func ChownTree(root string, o Owner) error {
	var firstErr error
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		uid, gid := owner(fi)
		if uid == o.UID && gid == o.GID {
			return nil
		}
		if err := os.Lchown(p, o.UID, o.GID); err != nil && firstErr == nil {
			firstErr = err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return firstErr
}
