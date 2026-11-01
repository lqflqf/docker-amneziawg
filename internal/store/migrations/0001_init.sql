-- Schema version 1. Every value is checked by awgctl before it is written;
-- the CHECK constraints are a second line of defence.
CREATE TABLE meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE peers (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL UNIQUE,
  octet         INTEGER NOT NULL UNIQUE CHECK (octet BETWEEN 2 AND 254),
  public_key    TEXT NOT NULL UNIQUE CHECK (length(public_key) = 44),
  private_key   TEXT NOT NULL CHECK (length(private_key) = 44),
  preshared_key TEXT CHECK (preshared_key IS NULL OR length(preshared_key) = 44),
  keepalive     INTEGER NOT NULL DEFAULT 1 CHECK (keepalive IN (0, 1)),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
