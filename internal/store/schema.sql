-- The whole schema. The DB holds only configuration: no counters, no
-- timestamps, no runtime state. awgctl checks every value before it writes
-- it; the CHECK constraints are a second line of defence.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- id is also the last octet of the peer address in the /24 tunnel subnet.
CREATE TABLE peers (
  id            INTEGER PRIMARY KEY CHECK (id BETWEEN 2 AND 254),
  name          TEXT NOT NULL UNIQUE,
  public_key    TEXT NOT NULL UNIQUE CHECK (length(public_key) = 44),
  private_key   TEXT NOT NULL CHECK (length(private_key) = 44),
  preshared_key TEXT CHECK (preshared_key IS NULL OR length(preshared_key) = 44),
  keepalive     INTEGER NOT NULL DEFAULT 1 CHECK (keepalive IN (0, 1)),
  enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1))
);
