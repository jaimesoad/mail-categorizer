package storage

// migrations are applied in order; index+1 is the schema version. They only
// ever grow: never edit a released migration, append a new one.
var migrations = []string{
	// 1 — initial schema (spec §5 plus the additive columns/tables documented in docs/PLAN.md §5)
	`
CREATE TABLE emails (
    id            TEXT PRIMARY KEY,
    message_id    TEXT,
    thread_id     TEXT,
    sender_email  TEXT,
    sender_name   TEXT,
    sender_domain TEXT,
    subject       TEXT,
    body_text     TEXT,
    received_at   DATETIME,
    raw_hash      TEXT UNIQUE,
    created_at    DATETIME NOT NULL,
    purged_at     DATETIME
);
CREATE INDEX idx_emails_message_id ON emails(message_id);
CREATE INDEX idx_emails_sender     ON emails(sender_email);
CREATE INDEX idx_emails_domain     ON emails(sender_domain);

CREATE TABLE email_features (
    email_id   TEXT NOT NULL,
    feature    TEXT NOT NULL,
    value      TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0,
    source     TEXT NOT NULL DEFAULT 'pattern',
    PRIMARY KEY (email_id, feature, value),
    FOREIGN KEY (email_id) REFERENCES emails(id) ON DELETE CASCADE
);
CREATE INDEX idx_features_lookup ON email_features(feature, value);

CREATE TABLE categories (
    id          TEXT PRIMARY KEY,
    parent_id   TEXT,
    name        TEXT NOT NULL,
    description TEXT,
    enabled     INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (parent_id) REFERENCES categories(id)
);

CREATE TABLE classifications (
    email_id    TEXT NOT NULL,
    category_id TEXT NOT NULL,
    score       REAL NOT NULL,
    method      TEXT NOT NULL,
    explanation TEXT,
    is_final    INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL,
    PRIMARY KEY (email_id, category_id),
    FOREIGN KEY (email_id)    REFERENCES emails(id) ON DELETE CASCADE,
    FOREIGN KEY (category_id) REFERENCES categories(id)
);
CREATE INDEX idx_class_final ON classifications(is_final, category_id);

CREATE TABLE questions (
    id         TEXT PRIMARY KEY,
    text       TEXT NOT NULL,
    feature    TEXT NOT NULL,
    type       TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    source     TEXT NOT NULL DEFAULT 'config',
    validated  INTEGER NOT NULL DEFAULT 1,
    definition TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE question_answers (
    id            TEXT PRIMARY KEY,
    question_id   TEXT NOT NULL,
    answer_value  TEXT NOT NULL,
    feature       TEXT NOT NULL,
    feature_value TEXT NOT NULL,
    FOREIGN KEY (question_id) REFERENCES questions(id) ON DELETE CASCADE
);

CREATE TABLE sessions (
    id               TEXT PRIMARY KEY,
    email_id         TEXT NOT NULL UNIQUE,
    status           TEXT NOT NULL,           -- open | resolved
    initial_decision TEXT NOT NULL,           -- know | predict | ask | unknown
    questions_asked  INTEGER NOT NULL DEFAULT 0,
    final_category   TEXT,
    resolved_by      TEXT,                    -- auto | predicted | answers | user | limit | unknown
    state            TEXT NOT NULL,           -- JSON (frozen evidence, candidates, asked, answers)
    created_at       DATETIME NOT NULL,
    updated_at       DATETIME NOT NULL,
    FOREIGN KEY (email_id) REFERENCES emails(id) ON DELETE CASCADE
);
CREATE INDEX idx_sessions_status ON sessions(status);

CREATE TABLE interactions (
    id                  TEXT PRIMARY KEY,
    email_id            TEXT NOT NULL,
    session_id          TEXT,
    kind                TEXT NOT NULL,        -- answer | skip | confirm | correct | manual
    question_id         TEXT,
    answer              TEXT,
    previous_prediction TEXT,
    resulting_category  TEXT,
    expected_gain       REAL,
    realized_gain       REAL,
    created_at          DATETIME NOT NULL,
    FOREIGN KEY (email_id)    REFERENCES emails(id) ON DELETE CASCADE,
    FOREIGN KEY (question_id) REFERENCES questions(id)
);
CREATE INDEX idx_inter_email ON interactions(email_id, kind);
CREATE INDEX idx_inter_question ON interactions(question_id, kind);

CREATE TABLE learned_rules (
    id            TEXT PRIMARY KEY,
    category_id   TEXT NOT NULL,
    key           TEXT NOT NULL,
    kind          TEXT NOT NULL,
    negative      INTEGER NOT NULL DEFAULT 0,
    conditions    TEXT NOT NULL,              -- JSON
    weight        REAL NOT NULL,
    observations  INTEGER NOT NULL DEFAULT 0,
    confirmations INTEGER NOT NULL DEFAULT 0,
    rejections    INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL,
    FOREIGN KEY (category_id) REFERENCES categories(id)
);
CREATE UNIQUE INDEX idx_rules_unique ON learned_rules(category_id, key, negative);
CREATE INDEX idx_rules_key ON learned_rules(key);
`,
}
