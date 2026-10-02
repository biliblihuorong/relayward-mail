-- 0003: per-app body injection switch (M4). The unsubscribe footer in the
-- message body defaults to on; existing apps keep it enabled.
ALTER TABLE apps ADD COLUMN body_injection INTEGER NOT NULL DEFAULT 1;
