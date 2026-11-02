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
)

func peer(name string, octet int) Peer {
	priv, pub := keys.Pair()
	return Peer{Name: name, Octet: octet, PrivateKey: priv, PublicKey: pub, PresharedKey: keys.NewSymmetric(), Keepalive: true, Enabled: true}
}

func TestCreateOpenAndPeers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	err := Create(path, func(tx *Tx) error {
		if err := tx.SetSetting("net.subnet", "10.1.2.0/24"); err != nil {
			return err
		}
		return tx.SetMeta("generation", "1")
	})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("db mode: %v %v", fi, err)
	}
	db, err := Open(path, filepath.Join(dir, "backups"))
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
	var ids []int64
	err = db.Update(ctx, func(tx *Tx) error {
		for i, n := range []string{"a", "b"} {
			id, err := tx.AddPeer(peer(n, i+2))
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return tx.DeletePeer(ids[1])
	})
	if err != nil {
		t.Fatal(err)
	}
	// AUTOINCREMENT: the id of a deleted peer is never used again.
	var next int64
	db.Update(ctx, func(tx *Tx) error {
		next, err = tx.AddPeer(peer("c", 3))
		return err
	})
	if next <= ids[1] {
		t.Fatalf("id %d reused (deleted %d)", next, ids[1])
	}
	db.View(ctx, func(tx *Tx) error {
		ps, _ := tx.Peers()
		if len(ps) != 2 || ps[0].Name != "a" || ps[1].Name != "c" {
			t.Fatalf("peers %+v", ps)
		}
		g, _ := tx.BumpGeneration()
		_ = g
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

func TestConstraintsAndNewerSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	if err := Create(path, func(tx *Tx) error {
		p := peer("a", 2)
		p.Octet = 255
		_, err := tx.AddPeer(p)
		if err == nil {
			t.Error("octet 255 accepted by the schema")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	db, _ := Open(path, dir)
	db.sql.Exec("PRAGMA user_version = 99")
	db.Close()
	if _, err := Open(path, dir); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("got %v, want ErrNewerSchema", err)
	}
}

func newDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	if err := Create(path, func(*Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(filepath.Join(dir, "missing.db"), dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: got %v, want ErrNotExist", err)
	}
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte(strings.Repeat("this is not a database ", 50)), 0o600)
	if _, err := Open(junk, dir); err == nil {
		t.Error("a file that is not a database was opened")
	}
}

func TestOpenEmptyFileRunsMigrations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	os.WriteFile(path, nil, 0o600)
	db, err := Open(path, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if v, err := db.Version(); err != nil || v != SchemaVersion {
		t.Fatalf("version %d %v, want %d", v, err, SchemaVersion)
	}
	// Schema 0 has no data, so there is nothing to back up.
	if _, err := os.Stat(filepath.Join(dir, "backups")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup dir made for schema 0: %v", err)
	}
}

func TestOpenFailedMigrationIsRolledBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "amneziawg.db")
	os.WriteFile(path, nil, 0o600)
	d, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.sql.Exec("CREATE TABLE meta (x TEXT)"); err != nil {
		t.Fatal(err)
	}
	d.Close()
	_, err = Open(path, dir)
	if err == nil || !strings.Contains(err.Error(), "0001_init.sql") {
		t.Fatalf("got %v, want an error that names the migration", err)
	}
	d, _ = open(path)
	defer d.Close()
	if v, _ := d.Version(); v != 0 {
		t.Fatalf("version %d after a failed migration, want 0", v)
	}
	var n int
	d.sql.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'peers'").Scan(&n)
	if n != 0 {
		t.Fatal("a failed migration left the peers table")
	}
}

func TestMigrateClosedDB(t *testing.T) {
	db := newDB(t)
	db.Close()
	if err := db.migrate(nil); err == nil {
		t.Fatal("migrate on a closed DB returned nil")
	}
}

func TestCreateErrors(t *testing.T) {
	dir := t.TempDir()
	if err := Create(filepath.Join(dir, "no", "such", "dir.db"), func(*Tx) error { return nil }); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing dir: got %v", err)
	}
	path := filepath.Join(dir, "amneziawg.db")
	os.WriteFile(path, []byte("keep"), 0o600)
	err := Create(path, func(tx *Tx) error { return tx.SetMeta("generation", "1") })
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

func TestVacuumInto(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	db.Update(ctx, func(tx *Tx) error { return tx.SetSetting("net.mtu", "1400") })
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := db.VacuumInto(dst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("copy mode: %v %v", fi, err)
	}
	c, err := Open(dst, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.View(ctx, func(tx *Tx) error {
		if s, _ := tx.Settings(); s["net.mtu"] != "1400" {
			t.Errorf("copy settings %v", s)
		}
		return nil
	})
	if err := db.VacuumInto(dst); err == nil {
		t.Fatal("VACUUM INTO an existing file returned nil")
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
		if _, err := tx.AddPeer(peer("a", 2)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the error of fn", err)
	}
	db.View(ctx, func(tx *Tx) error { return tx.SetMeta("generation", "7") })
	db.View(ctx, func(tx *Tx) error {
		s, _ := tx.Settings()
		ps, _ := tx.Peers()
		g, _ := tx.Generation()
		if len(s) != 0 || len(ps) != 0 || g != 0 {
			t.Errorf("rolled back data is visible: %v %v %d", s, ps, g)
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

func TestMetaAndGeneration(t *testing.T) {
	db := newDB(t)
	err := db.Update(context.Background(), func(tx *Tx) error {
		if v, err := tx.Meta("missing"); v != "" || err != nil {
			t.Errorf("missing meta: %q %v", v, err)
		}
		for want := int64(1); want <= 3; want++ {
			if g, err := tx.BumpGeneration(); g != want || err != nil {
				t.Errorf("bump: %d %v, want %d", g, err, want)
			}
		}
		if v, _ := tx.Meta("generation"); v != "3" {
			t.Errorf("generation meta %q", v)
		}
		tx.SetMeta("generation", "x")
		if _, err := tx.Generation(); err == nil {
			t.Error("a generation that is not a number was accepted")
		}
		if _, err := tx.BumpGeneration(); err == nil {
			t.Error("bump of a generation that is not a number returned nil")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSettingsAndReplacePrefix(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	stamp := func(tx *Tx, k string) string {
		var s string
		tx.tx.QueryRow("SELECT updated_at FROM settings WHERE key = ?", k).Scan(&s)
		return s
	}
	err := db.Update(ctx, func(tx *Tx) error {
		for k, v := range map[string]string{"net.mtu": "1400", "awg.jc": "4", "awg.s1": "20", "awg.i2": "<r 5>"} {
			if err := tx.SetSetting(k, v); err != nil {
				return err
			}
		}
		tx.tx.Exec("UPDATE settings SET updated_at = 'old'")
		if err := tx.SetSetting("net.mtu", "1400"); err != nil {
			return err
		}
		if s := stamp(tx, "net.mtu"); s != "old" {
			t.Errorf("same value changed updated_at to %q", s)
		}
		if err := tx.SetSetting("net.mtu", "1300"); err != nil {
			return err
		}
		if s := stamp(tx, "net.mtu"); s == "old" {
			t.Error("new value kept the old updated_at")
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
	var a, b Peer
	err := db.Update(ctx, func(tx *Tx) error {
		var err error
		a, b = peer("a", 2), peer("b", 3)
		if a.ID, err = tx.AddPeer(a); err != nil {
			return err
		}
		b.PresharedKey = ""
		b.ID, err = tx.AddPeer(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(ctx, func(tx *Tx) error {
		a.Name, a.Octet, a.Enabled, a.Keepalive, a.PresharedKey = "phone", 9, false, false, ""
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
		if got.Name != "phone" || got.Octet != 9 || got.Enabled || got.Keepalive || got.PresharedKey != "" ||
			got.PublicKey != a.PublicKey || got.CreatedAt == "" || got.UpdatedAt == "" {
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
		if err := tx.UpdatePeer(ghost); err == nil || err.Error() != "peer #999 does not exist" {
			t.Errorf("update of a missing peer: %v", err)
		}
		if err := tx.DeletePeer(999); err == nil || err.Error() != "peer #999 does not exist" {
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
	p.ID = 1
	_, errAdd := done.AddPeer(p)
	_, errMeta := done.Meta("generation")
	_, errGen := done.Generation()
	_, errBump := done.BumpGeneration()
	_, errSettings := done.Settings()
	_, errPeers := done.Peers()
	for name, err := range map[string]error{
		"AddPeer":        errAdd,
		"Meta":           errMeta,
		"Generation":     errGen,
		"BumpGeneration": errBump,
		"Settings":       errSettings,
		"Peers":          errPeers,
		"SetMeta":        done.SetMeta("k", "v"),
		"SetSetting":     done.SetSetting("k", "v"),
		"delete":         done.SetSetting("k", ""),
		"ReplacePrefix":  done.ReplacePrefix("awg.", nil),
		"UpdatePeer":     done.UpdatePeer(p),
		"DeletePeer":     done.DeletePeer(1),
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
		p := peer("a", 2)
		// SQLite keeps 2.5 as a REAL in an INTEGER column, and 2.5 passes the CHECK.
		_, err := tx.tx.Exec(`INSERT INTO peers(name, octet, public_key, private_key, created_at, updated_at)
			VALUES('a', 2.5, ?, ?, 'x', 'x')`, p.PublicKey, p.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.tx.Exec("INSERT INTO settings(key, value, updated_at) VALUES(NULL, 'v', 'x')"); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	db.View(ctx, func(tx *Tx) error {
		if ps, err := tx.Peers(); err == nil {
			t.Errorf("a REAL octet was read as %+v", ps)
		}
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
