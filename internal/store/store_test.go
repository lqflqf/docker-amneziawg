package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lqflqf/docker-amneziawg/internal/keys"
	"golang.org/x/sys/unix"
)

func peer(name string, id int) Peer {
	priv, pub := keys.Pair()
	return Peer{ID: id, Name: name, PrivateKey: priv, PublicKey: pub, PresharedKey: keys.NewSymmetric(), Keepalive: true, Enabled: true}
}

func TestCreateOpenAndPeers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	err := Create(path, func(tx *Tx) error { return tx.SetSetting("net.subnet", "10.1.2.0/24") })
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode: %v %v", fi, err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var mode string
	db.sql.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "delete" {
		t.Fatalf("journal_mode = %s, want delete (no -wal file)", mode)
	}
	ctx := context.Background()
	err = db.Update(ctx, func(tx *Tx) error {
		for i, n := range []string{"b", "a"} {
			if err := tx.AddPeer(peer(n, 3-i)); err != nil {
				return err
			}
		}
		return tx.DeletePeer(3)
	})
	if err != nil {
		t.Fatal(err)
	}
	// The id is the address octet: a new peer can take the id of a deleted one.
	if err := db.Update(ctx, func(tx *Tx) error { return tx.AddPeer(peer("c", 3)) }); err != nil {
		t.Fatal(err)
	}
	db.View(ctx, func(tx *Tx) error {
		ps, _ := tx.Peers()
		if len(ps) != 2 || ps[0].ID != 2 || ps[0].Name != "a" || ps[1].ID != 3 || ps[1].Name != "c" {
			t.Fatalf("peers %+v", ps)
		}
		return nil
	})
}

func TestCreateFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	err := Create(path, func(tx *Tx) error { return errors.New("boom") })
	if err == nil {
		t.Fatal("no error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestCreateDirSyncFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	orig := syncDir
	t.Cleanup(func() { syncDir = orig })
	syncDir = func(string) error { return errors.New("sync failed") }
	if err := Create(path, func(*Tx) error { return nil }); err == nil || !strings.Contains(err.Error(), "sync failed") {
		t.Fatalf("got %v, want the sync error", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestCreateNeverReplacesAnotherDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	orig := renameat2
	t.Cleanup(func() { renameat2 = orig })
	// Another creator wins the race after the existence checks.
	renameat2 = func(od int, op string, nd int, np string, fl uint) error {
		os.WriteFile(path, []byte("other"), 0o600)
		return orig(od, op, nd, np, fl)
	}
	if err := Create(path, func(*Tx) error { return nil }); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v, want already exists", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "other" {
		t.Fatalf("the other DB was replaced: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestCreateWithoutRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	orig := renameat2
	t.Cleanup(func() { renameat2 = orig })
	renameat2 = func(int, string, int, string, uint) error { return unix.EINVAL }
	if err := Create(path, func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if err := Create(path, func(*Tx) error { return nil }); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v, want already exists", err)
	}
	renameat2 = func(int, string, int, string, uint) error { return unix.EACCES }
	if err := Create(filepath.Join(dir, "x.db"), func(*Tx) error { return nil }); !errors.Is(err, unix.EACCES) {
		t.Fatalf("got %v, want EACCES", err)
	}
}

func TestConstraints(t *testing.T) {
	db := newDB(t)
	err := db.Update(context.Background(), func(tx *Tx) error {
		for _, id := range []int{1, 255} {
			if err := tx.AddPeer(peer("a", id)); err == nil {
				t.Errorf("id %d accepted by the schema", id)
			}
		}
		if err := tx.AddPeer(peer("a", 2)); err != nil {
			return err
		}
		if err := tx.AddPeer(peer("b", 2)); err == nil {
			t.Error("a duplicate id was accepted")
		}
		if err := tx.AddPeer(peer("a", 3)); err == nil {
			t.Error("a duplicate name was accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	if err := Create(path, func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "missing.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: got %v, want ErrNotExist", err)
	}
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte(strings.Repeat("this is not a database ", 50)), 0o600)
	if _, err := Open(junk); err == nil {
		t.Error("a file that is not a database was opened")
	}
}

func TestCreateErrors(t *testing.T) {
	dir := t.TempDir()
	if err := Create(filepath.Join(dir, "no", "such", "dir.db"), func(*Tx) error { return nil }); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing dir: got %v", err)
	}
	path := filepath.Join(dir, "amneziawg.db")
	os.WriteFile(path, []byte("keep"), 0o600)
	err := Create(path, func(tx *Tx) error { return tx.SetSetting("net.mtu", "1400") })
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v, want already exists", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Fatalf("existing file changed: %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestUpdateRollsBackAndViewNeverCommits(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.Update(ctx, func(tx *Tx) error {
		if err := tx.SetSetting("net.mtu", "1400"); err != nil {
			return err
		}
		if err := tx.AddPeer(peer("a", 2)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the error of fn", err)
	}
	db.View(ctx, func(tx *Tx) error { return tx.SetSetting("net.subnet", "10.9.9.0/24") })
	db.View(ctx, func(tx *Tx) error {
		s, _ := tx.Settings()
		ps, _ := tx.Peers()
		if len(s) != 0 || len(ps) != 0 {
			t.Errorf("rolled back data is visible: %v %v", s, ps)
		}
		return nil
	})
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.Update(cancelled, func(*Tx) error { return nil }); err == nil {
		t.Error("Update with a cancelled context returned nil")
	}
	if err := db.View(cancelled, func(*Tx) error { return nil }); err == nil {
		t.Error("View with a cancelled context returned nil")
	}
}

func TestViewBlocksCommit(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	other, err := open(db.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.sql.Exec("PRAGMA busy_timeout = 0"); err != nil {
		t.Fatal(err)
	}
	// init renders inside one View: a command must not commit in between.
	db.View(ctx, func(tx *Tx) error {
		if _, err := tx.Settings(); err != nil {
			t.Fatal(err)
		}
		err := other.Update(ctx, func(tx *Tx) error { return tx.SetSetting("net.mtu", "1400") })
		if err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("a commit during a View: got %v, want database is locked", err)
		}
		return nil
	})
	if err := other.Update(ctx, func(tx *Tx) error { return tx.SetSetting("net.mtu", "1400") }); err != nil {
		t.Errorf("a commit after the View: %v", err)
	}
}

func TestSettingsAndReplacePrefix(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	err := db.Update(ctx, func(tx *Tx) error {
		for k, v := range map[string]string{"net.mtu": "1400", "awg.jc": "4", "awg.s1": "20", "awg.i2": "<r 5>"} {
			if err := tx.SetSetting(k, v); err != nil {
				return err
			}
		}
		if err := tx.SetSetting("net.mtu", "1300"); err != nil {
			return err
		}
		return tx.ReplacePrefix("awg.", map[string]string{"jc": "5", "s1": "20", "h1": "10"})
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"net.mtu": "1300", "awg.jc": "5", "awg.s1": "20", "awg.h1": "10"}
	db.View(ctx, func(tx *Tx) error {
		s, err := tx.Settings()
		if err != nil || len(s) != len(want) {
			t.Fatalf("settings %v %v, want %v", s, err, want)
		}
		for k, v := range want {
			if s[k] != v {
				t.Errorf("%s = %q, want %q", k, s[k], v)
			}
		}
		return nil
	})
	db.Update(ctx, func(tx *Tx) error { return tx.SetSetting("net.mtu", "") })
	db.View(ctx, func(tx *Tx) error {
		if s, _ := tx.Settings(); s["net.mtu"] != "" || len(s) != 3 {
			t.Errorf("empty value did not delete the row: %v", s)
		}
		return nil
	})
}

func TestUpdateAndDeletePeer(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	a, b := peer("a", 2), peer("b", 3)
	b.PresharedKey = ""
	err := db.Update(ctx, func(tx *Tx) error {
		if err := tx.AddPeer(a); err != nil {
			return err
		}
		return tx.AddPeer(b)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(ctx, func(tx *Tx) error {
		a.Name, a.Enabled, a.Keepalive, a.PresharedKey = "phone", false, false, ""
		return tx.UpdatePeer(a)
	})
	if err != nil {
		t.Fatal(err)
	}
	db.View(ctx, func(tx *Tx) error {
		ps, err := tx.Peers()
		if err != nil || len(ps) != 2 {
			t.Fatalf("peers %v %v", ps, err)
		}
		got := ps[0]
		if got != a {
			t.Errorf("updated peer %+v", got)
		}
		if ps[1].PresharedKey != "" || !ps[1].Enabled {
			t.Errorf("peer without PSK %+v", ps[1])
		}
		var null bool
		tx.tx.QueryRow("SELECT preshared_key IS NULL FROM peers WHERE id = ?", b.ID).Scan(&null)
		if !null {
			t.Error("an empty PSK is not stored as NULL")
		}
		return nil
	})
	db.Update(ctx, func(tx *Tx) error {
		ghost := a
		ghost.ID = 999
		if err := tx.UpdatePeer(ghost); err == nil || err.Error() != "peer 999 does not exist" {
			t.Errorf("update of a missing peer: %v", err)
		}
		if err := tx.DeletePeer(999); err == nil || err.Error() != "peer 999 does not exist" {
			t.Errorf("delete of a missing peer: %v", err)
		}
		dup := a
		dup.Name = "b"
		if err := tx.UpdatePeer(dup); err == nil {
			t.Error("a duplicate name was accepted")
		}
		return nil
	})
}

func TestTxAfterCommitFails(t *testing.T) {
	db := newDB(t)
	var done *Tx
	db.Update(context.Background(), func(tx *Tx) error { done = tx; return nil })
	p := peer("a", 2)
	_, errSettings := done.Settings()
	_, errPeers := done.Peers()
	for name, err := range map[string]error{
		"AddPeer":       done.AddPeer(p),
		"Settings":      errSettings,
		"Peers":         errPeers,
		"SetSetting":    done.SetSetting("k", "v"),
		"delete":        done.SetSetting("k", ""),
		"ReplacePrefix": done.ReplacePrefix("awg.", nil),
		"UpdatePeer":    done.UpdatePeer(p),
		"DeletePeer":    done.DeletePeer(2),
	} {
		if !errors.Is(err, sql.ErrTxDone) {
			t.Errorf("%s after commit: got %v, want ErrTxDone", name, err)
		}
	}
}

func TestCreateTempFileRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	err := Create(path, func(*Tx) error {
		tmp, _ := filepath.Glob(filepath.Join(dir, ".amneziawg.db.seed-*"))
		if len(tmp) != 1 {
			t.Fatalf("temporary files %v", tmp)
		}
		return os.Remove(tmp[0])
	})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want ErrNotExist", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestBadRowsFailScan(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	db.Update(ctx, func(tx *Tx) error {
		if _, err := tx.tx.Exec("INSERT INTO settings(key, value) VALUES(NULL, 'v')"); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	db.View(ctx, func(tx *Tx) error {
		if s, err := tx.Settings(); err == nil {
			t.Errorf("a NULL key was read as %v", s)
		}
		if err := tx.ReplacePrefix("awg.", nil); err == nil {
			t.Error("ReplacePrefix ignored the Settings error")
		}
		return nil
	})
}

func TestReplacePrefixStopsAtFirstError(t *testing.T) {
	for _, c := range []struct{ event, values string }{
		{"DELETE", ""},
		{"INSERT", "jc"},
	} {
		t.Run(c.event, func(t *testing.T) {
			db := newDB(t)
			ctx := context.Background()
			db.Update(ctx, func(tx *Tx) error { return tx.SetSetting("awg.s1", "20") })
			db.sql.Exec("CREATE TRIGGER deny BEFORE " + c.event + " ON settings BEGIN SELECT RAISE(ABORT, 'denied'); END")
			values := map[string]string{}
			if c.values != "" {
				values = map[string]string{c.values: "5", "s1": "20"}
			}
			err := db.Update(ctx, func(tx *Tx) error { return tx.ReplacePrefix("awg.", values) })
			if err == nil || !strings.Contains(err.Error(), "denied") {
				t.Fatalf("got %v, want the trigger error", err)
			}
			db.View(ctx, func(tx *Tx) error {
				if s, _ := tx.Settings(); len(s) != 1 || s["awg.s1"] != "20" {
					t.Errorf("settings changed after an error: %v", s)
				}
				return nil
			})
		})
	}
}
