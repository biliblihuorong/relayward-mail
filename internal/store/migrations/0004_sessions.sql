-- M6: browser login sessions for the management page. One row per login;
-- the cookie carries the plaintext secret, only its SHA-256 hash is stored.
-- A session dies at its expiry, when its token is revoked (cascade delete in
-- code), or as soon as the token row disappears (join enforced in code).

CREATE TABLE admin_sessions (
  id         INTEGER PRIMARY KEY,
  token_hash TEXT UNIQUE NOT NULL, -- SHA-256 of the cookie value
  token_id   INTEGER NOT NULL,     -- admin_tokens.id the login was made from
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_seen  INTEGER NOT NULL
);
CREATE INDEX idx_sessions_expiry ON admin_sessions(expires_at);
