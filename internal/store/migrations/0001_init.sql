-- Initial schema for relayward, per the plan's data model.
-- Times are stored as UTC Unix seconds.

CREATE TABLE apps (
  id               INTEGER PRIMARY KEY,
  name             TEXT UNIQUE NOT NULL,      -- 即 SMTP 用户名
  password_hash    TEXT NOT NULL,             -- argon2id
  enabled          INTEGER NOT NULL DEFAULT 1,
  unsubscribe      INTEGER NOT NULL DEFAULT 1, -- 退订开关
  allowed_from     TEXT NOT NULL,             -- JSON 数组
  rate_per_hour    INTEGER NOT NULL DEFAULT 500,
  display_name     TEXT,                      -- 退订页显示的程序名
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL
);

CREATE TABLE messages (
  id            INTEGER PRIMARY KEY,
  app_id        INTEGER NOT NULL,
  ts            INTEGER NOT NULL,
  mail_from     TEXT NOT NULL,
  rcpt_to       TEXT NOT NULL,
  subject       TEXT,
  size          INTEGER,
  message_id    TEXT,
  status        TEXT NOT NULL,   -- sent / suppressed / rate_limited / failed
  upstream_resp TEXT,
  client_ip     TEXT
);
CREATE INDEX idx_msg_app_ts ON messages(app_id, ts);
CREATE INDEX idx_msg_rcpt   ON messages(rcpt_to);

CREATE TABLE unsubscribes (
  id       INTEGER PRIMARY KEY,
  app_id   INTEGER NOT NULL,
  email    TEXT NOT NULL,          -- 小写规范化
  ts       INTEGER NOT NULL,
  source   TEXT NOT NULL,          -- link / one_click / api
  UNIQUE(app_id, email)
);

CREATE TABLE admin_tokens (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL,
  token_hash  TEXT UNIQUE NOT NULL, -- SHA-256
  role        TEXT NOT NULL,        -- admin / operator / viewer
  expires_at  INTEGER,
  last_used   INTEGER,
  created_by  INTEGER,
  created_at  INTEGER NOT NULL
);

CREATE TABLE audit_log (
  id        INTEGER PRIMARY KEY,
  ts        INTEGER NOT NULL,
  token_id  INTEGER,
  ip        TEXT,
  action    TEXT NOT NULL,   -- app.create / app.rotate / token.revoke ...
  target    TEXT,
  detail    TEXT             -- JSON
);
