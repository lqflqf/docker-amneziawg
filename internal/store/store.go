// Package store keeps the desired state in SQLite with plain database/sql.
// There is no ORM: three tables, explicit transactions and embedded migrations.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go, no cgo
)

//go:embed migrations/*.sql
var migrations embed.FS

// SchemaVersion is the newest schema this binary knows.
var SchemaVersion = func() int {
	n, _ := migrationList()
	return len(n)
}()

// ErrNewerSchema means the DB was written by a newer awgctl.
var ErrNewerSchema = errors.New("the database was written by a newer version of this image")

// Peer is one row of the peers table.
type Peer struct {
	ID           int64
	Name         string
	Octet        int
	PublicKey    string
	PrivateKey   string
	PresharedKey string // "" = none (legacy peers only)
	Keepalive    bool
	Enabled      bool
	CreatedAt    string
	UpdatedAt    string
}

// DB is an open database.
type DB struct {
	sql  *sql.DB
	Path string
}

func dsn(path string) string {
	// DELETE journal: no -wal/-shm files, so one file is a complete copy when
	// no command runs. Commands are short and serialized by the awgctl lock.
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

func migrationList() ([]string, error) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	sort.Strings(names)
	return names, err
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

// Version returns the schema version of the DB.
func (d *DB) Version() (int, error) { return userVersion(d.sql) }

// migrate runs the migrations above the current user_version, each in its
// own transaction. backup, if not nil, runs first when there is work to do.
func (d *DB) migrate(backup func(from int) error) error {
	cur, err := userVersion(d.sql)
	if err != nil {
		return err
	}
	names, err := migrationList()
	if err != nil {
		return err
	}
	if cur > len(names) {
		return fmt.Errorf("%w (schema %d, this image knows %d)", ErrNewerSchema, cur, len(names))
	}
	if cur == len(names) {
		return nil
	}
	if backup != nil && cur > 0 {
		if err := backup(cur); err != nil {
			return fmt.Errorf("pre-upgrade copy: %w", err)
		}
	}
	for i := cur; i < len(names); i++ {
		body, err := migrations.ReadFile(names[i])
		if err != nil {
			return err
		}
		tx, err := d.sql.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("%s: %w", names[i], err)
		}
		if _, err := tx.Exec("PRAGMA user_version = " + strconv.Itoa(i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Open opens an existing DB and upgrades its schema. backupDir receives a
// VACUUM INTO copy before an upgrade.
func Open(path, backupDir string) (*DB, error) {
	if _, err := os.Lstat(path); err != nil {
		return nil, err
	}
	d, err := open(path)
	if err != nil {
		return nil, err
	}
	err = d.migrate(func(from int) error {
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return err
		}
		dst := filepath.Join(backupDir, fmt.Sprintf("pre-upgrade-v%d-%s.db", from, time.Now().UTC().Format("20060102T150405Z")))
		return d.VacuumInto(dst)
	})
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// VacuumInto writes a consistent copy of the DB to dst (mode 600).
func (d *DB) VacuumInto(dst string) error {
	if _, err := d.sql.Exec("VACUUM INTO ?", dst); err != nil {
		return err
	}
	return os.Chmod(dst, 0o600)
}

// Create makes a new DB at path atomically: it is built in a temporary file
// next to path, filled by fn in one transaction, synced and renamed. On any
// failure nothing is left at path.
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
	if err = d.migrate(nil); err != nil {
		d.Close()
		return err
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
	if _, statErr := os.Lstat(path); statErr == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncFile(dir)
}

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

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Meta returns a meta value ("" if missing).
func (t *Tx) Meta(key string) (string, error) {
	var v string
	err := t.tx.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetMeta sets a meta value.
func (t *Tx) SetMeta(key, value string) error {
	_, err := t.tx.Exec("INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// Generation returns the commit counter.
func (t *Tx) Generation() (int64, error) {
	v, err := t.Meta("generation")
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// BumpGeneration adds one to the commit counter and returns the new value.
func (t *Tx) BumpGeneration() (int64, error) {
	g, err := t.Generation()
	if err != nil {
		return 0, err
	}
	g++
	return g, t.SetMeta("generation", strconv.FormatInt(g, 10))
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
	_, err := t.tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
		WHERE settings.value IS NOT excluded.value`, key, value, now())
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

const peerCols = "id, name, octet, public_key, private_key, COALESCE(preshared_key, ''), keepalive, enabled, created_at, updated_at"

func scanPeer(s interface{ Scan(...any) error }) (Peer, error) {
	var p Peer
	err := s.Scan(&p.ID, &p.Name, &p.Octet, &p.PublicKey, &p.PrivateKey, &p.PresharedKey,
		&p.Keepalive, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
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

// AddPeer inserts a peer and returns its id.
func (t *Tx) AddPeer(p Peer) (int64, error) {
	ts := now()
	res, err := t.tx.Exec(`INSERT INTO peers(name, octet, public_key, private_key, preshared_key,
		keepalive, enabled, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Octet, p.PublicKey, p.PrivateKey, nullable(p.PresharedKey), p.Keepalive, p.Enabled, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePeer writes every column of an existing peer.
func (t *Tx) UpdatePeer(p Peer) error {
	res, err := t.tx.Exec(`UPDATE peers SET name = ?, octet = ?, public_key = ?, private_key = ?,
		preshared_key = ?, keepalive = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Octet, p.PublicKey, p.PrivateKey, nullable(p.PresharedKey), p.Keepalive, p.Enabled, now(), p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("peer #%d does not exist", p.ID)
	}
	return nil
}

// DeletePeer removes a peer and its keys.
func (t *Tx) DeletePeer(id int64) error {
	res, err := t.tx.Exec("DELETE FROM peers WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("peer #%d does not exist", id)
	}
	return nil
}
