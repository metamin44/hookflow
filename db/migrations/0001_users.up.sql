-- Migration unit 1: users (database-schema.md §4.1, §7).
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE users (
    id                 uuid        PRIMARY KEY,
    email              text        NOT NULL,
    name               text        NOT NULL,
    password_hash      text,
    password_updated_at timestamptz,
    status             text        NOT NULL,
    mfa_enabled        boolean     NOT NULL DEFAULT false,
    last_login_at      timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT chk_users_email_len CHECK (char_length(email) BETWEEN 3 AND 254),
    CONSTRAINT chk_users_name_len  CHECK (char_length(name) BETWEEN 1 AND 100),
    CONSTRAINT chk_users_status    CHECK (status IN ('invited', 'active', 'disabled'))
);

CREATE UNIQUE INDEX uq_users_email_lower ON users (lower(email));
