-- Migration unit 5: endpoints (database-schema.md §4.5, §7).
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE endpoints (
    id               uuid        PRIMARY KEY,
    organization_id  uuid        NOT NULL,
    name             text        NOT NULL,
    description      text,
    url              text        NOT NULL,
    secret_encrypted text        NOT NULL,
    signing_scheme   text        NOT NULL DEFAULT 'hmac_sha256',
    http_headers     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status           text        NOT NULL DEFAULT 'active',
    timeout_ms       integer     NOT NULL DEFAULT 5000,
    max_attempts     integer,
    last_success_at  timestamptz,
    last_failure_at  timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz,

    CONSTRAINT fk_endpoints_org FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,

    CONSTRAINT chk_endpoints_name_len        CHECK (char_length(name) BETWEEN 1 AND 120),
    CONSTRAINT chk_endpoints_description_len CHECK (description IS NULL OR char_length(description) <= 500),
    CONSTRAINT chk_endpoints_url             CHECK (url ~ '^https://[^[:space:]]+$'),
    CONSTRAINT chk_endpoints_status          CHECK (status IN ('active', 'paused')),
    CONSTRAINT chk_endpoints_timeout_ms      CHECK (timeout_ms BETWEEN 250 AND 30000),
    CONSTRAINT chk_endpoints_max_attempts    CHECK (max_attempts IS NULL OR max_attempts BETWEEN 1 AND 20),
    CONSTRAINT uq_endpoints_org_id           UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX uq_endpoints_live_name
    ON endpoints (organization_id, name)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_endpoints_org_live
    ON endpoints (organization_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_endpoints_org_deleted
    ON endpoints (organization_id, deleted_at);
