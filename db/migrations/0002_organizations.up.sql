-- Migration unit 2: organizations (database-schema.md §4.2, §7).
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE organizations (
    id         uuid        PRIMARY KEY,
    name       text        NOT NULL,
    settings   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status     text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT chk_organizations_name_len CHECK (char_length(name) BETWEEN 1 AND 120),
    CONSTRAINT chk_organizations_status   CHECK (status IN ('active', 'suspended', 'archived'))
);
