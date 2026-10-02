-- M2 management API: soft deletion for apps (deleted apps keep their rows so
-- message logs stay joinable) and the token name recorded at audit time
-- (tokens may be revoked later, the join would lose the name).

ALTER TABLE apps ADD COLUMN deleted_at INTEGER;

ALTER TABLE audit_log ADD COLUMN token_name TEXT;
