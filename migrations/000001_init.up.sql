CREATE TABLE users (
    id            TEXT PRIMARY KEY,              -- UUID v4
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,                 -- bcrypt
    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at    DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    owner_id    TEXT NOT NULL REFERENCES users(id),
    assignee_id TEXT REFERENCES users(id),        -- nullable, diisi via /assign
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'todo'
                CHECK (status IN ('todo','in_progress','done')),
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_tasks_owner_status ON tasks(owner_id, status);
CREATE INDEX idx_tasks_owner_title  ON tasks(owner_id, title);   -- search by title
CREATE INDEX idx_tasks_assignee     ON tasks(assignee_id);

CREATE TABLE task_logs (
    id         TEXT PRIMARY KEY,
    task_id    TEXT NOT NULL REFERENCES tasks(id),
    actor_id   TEXT NOT NULL REFERENCES users(id),
    action     TEXT NOT NULL,                     -- 'created' | 'assigned' | ...
    payload    TEXT NOT NULL DEFAULT '{}',        -- JSON detail
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_task_logs_task ON task_logs(task_id, created_at);

CREATE TABLE idempotency_keys (
    key             TEXT NOT NULL,
    user_id         TEXT NOT NULL REFERENCES users(id),
    task_id         TEXT NOT NULL REFERENCES tasks(id),
    response_status INTEGER NOT NULL,           -- snapshot status pertama (201)
    response_body   TEXT NOT NULL,              -- snapshot JSON → replay identik byte-per-byte
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    expires_at      DATETIME NOT NULL,          -- created_at + 24h
    PRIMARY KEY (key, user_id)                  -- unique constraint = senjata anti-race
);
