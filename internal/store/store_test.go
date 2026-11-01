package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
