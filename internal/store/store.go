// Package store keeps the configuration in SQLite with plain database/sql.
// There is no ORM: two tables, explicit transactions and one embedded schema.
// The DB holds only configuration; there are no migrations.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go, no cgo
)

//go:embed schema.sql
var schema string

// Peer is one row of the peers table. ID is also the last octet of the peer
// address.
type Peer struct {
	ID           int
	Name         string
	PublicKey    string
	PrivateKey   string
	PresharedKey string // "" = none (legacy peers only)
	Keepalive    bool
	Enabled      bool
}

// DB is an open database.
type DB struct {
	sql  *sql.DB
	Path string
}

func dsn(path string) string {
	// DELETE journal: no -wal/-shm files, so one file is a complete copy when
	// no command runs. SQLite serializes the writers (BEGIN IMMEDIATE).
	return "file:" + path +
		"?_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)" +
		"&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
}

func open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{sql: db, Path: path}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.sql.Close() }

// Open opens an existing DB.
func Open(path string) (*DB, error) {
	if _, err := os.Lstat(path); err != nil {
		return nil, err
	}
	return open(path)
}

// Create makes a new DB at path atomically: it is built in a temporary file
// next to path, filled by fn in one transaction, synced and renamed. The
// rename never replaces a file at path. On any failure nothing is left at path.
func Create(path string, fn func(*Tx) error) (err error) {
	dir := filepath.Dir(path)
	rnd := make([]byte, 6)
	rand.Read(rnd)
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".seed-"+hex.EncodeToString(rnd))
	f, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	defer func() {
		if err != nil {
			os.Remove(tmp)
			os.Remove(tmp + "-journal")
		}
	}()
	d, err := open(tmp)
	if err != nil {
		return err
	}
	if _, err = d.sql.Exec(schema); err != nil {
		d.Close()
		return fmt.Errorf("schema: %w", err)
	}
	if err = d.Update(context.Background(), fn); err != nil {
		d.Close()
		return err
	}
	if err = d.Close(); err != nil {
		return err
	}
	if err = syncFile(tmp); err != nil {
		return err
	}
	if err = renameNoReplace(tmp, path); err != nil {
		return err
	}
	if err = syncDir(dir); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

// renameat2 is a variable, so that a test can add a race or an old file system.
var renameat2 = unix.Renameat2

// renameNoReplace renames src to dst and never replaces dst. Two creators
// (init and import-legacy in another container) can run at the same time.
func renameNoReplace(src, dst string) error {
	err := renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.EEXIST):
		return fmt.Errorf("%s already exists", dst)
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EOPNOTSUPP):
		// The file system has no RENAME_NOREPLACE (for example NFS): check, then rename.
		if _, statErr := os.Lstat(dst); statErr == nil {
			return fmt.Errorf("%s already exists", dst)
		}
		return os.Rename(src, dst)
	default:
		return fmt.Errorf("rename %s: %w", src, err)
	}
}

// syncDir is a variable, so that a test can make the directory sync fail.
var syncDir = syncFile

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Tx is one transaction.
type Tx struct{ tx *sql.Tx }

// Update runs fn in a write transaction (BEGIN IMMEDIATE) and commits when fn
// returns nil.
func (d *DB) Update(ctx context.Context, fn func(*Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Tx{tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// View runs fn in a transaction that is always rolled back.
func (d *DB) View(ctx context.Context, fn func(*Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(&Tx{tx})
}

// Settings returns every setting row.
func (t *Tx) Settings() (map[string]string, error) {
	rows, err := t.tx.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetSetting writes a setting; an empty value deletes the row.
func (t *Tx) SetSetting(key, value string) error {
	if value == "" {
		_, err := t.tx.Exec("DELETE FROM settings WHERE key = ?", key)
		return err
	}
	_, err := t.tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ReplacePrefix makes the rows under prefix equal to values (keys without
// the prefix). Rows not in values are deleted.
func (t *Tx) ReplacePrefix(prefix string, values map[string]string) error {
	cur, err := t.Settings()
	if err != nil {
		return err
	}
	for k := range cur {
		if strings.HasPrefix(k, prefix) {
			if _, ok := values[strings.TrimPrefix(k, prefix)]; !ok {
				if err := t.SetSetting(k, ""); err != nil {
					return err
				}
			}
		}
	}
	for k, v := range values {
		if err := t.SetSetting(prefix+k, v); err != nil {
			return err
		}
	}
	return nil
}

const peerCols = "id, name, public_key, private_key, COALESCE(preshared_key, ''), keepalive, enabled"

func scanPeer(s interface{ Scan(...any) error }) (Peer, error) {
	var p Peer
	err := s.Scan(&p.ID, &p.Name, &p.PublicKey, &p.PrivateKey, &p.PresharedKey, &p.Keepalive, &p.Enabled)
	return p, err
}

// Peers returns every peer, ordered by id.
func (t *Tx) Peers() ([]Peer, error) {
	rows, err := t.tx.Query("SELECT " + peerCols + " FROM peers ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// AddPeer inserts a peer with its own id.
func (t *Tx) AddPeer(p Peer) error {
	_, err := t.tx.Exec(`INSERT INTO peers(id, name, public_key, private_key, preshared_key, keepalive, enabled)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.PublicKey, p.PrivateKey, nullable(p.PresharedKey), p.Keepalive, p.Enabled)
	return err
}

// UpdatePeer writes every column of an existing peer.
func (t *Tx) UpdatePeer(p Peer) error {
	res, err := t.tx.Exec(`UPDATE peers SET name = ?, public_key = ?, private_key = ?,
		preshared_key = ?, keepalive = ?, enabled = ? WHERE id = ?`,
		p.Name, p.PublicKey, p.PrivateKey, nullable(p.PresharedKey), p.Keepalive, p.Enabled, p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("peer %d does not exist", p.ID)
	}
	return nil
}

// DeletePeer removes a peer and its keys.
func (t *Tx) DeletePeer(id int) error {
	res, err := t.tx.Exec("DELETE FROM peers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("peer %d does not exist", id)
	}
	return nil
}
