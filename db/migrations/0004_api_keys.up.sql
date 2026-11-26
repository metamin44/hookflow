-- Migration unit 4: api_keys (database-schema.md §4.4, §7).
-- users -> api_keys is the documented single-column FK exception (F3-A):
-- users is global and has no organization_id; membership validation is application-level.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE api_keys (
    id                  uuid        PRIMARY KEY,
    organization_id     uuid        NOT NULL,
    name                text        NOT NULL,
    key_prefix          text        NOT NULL,
    secret_hash         text        NOT NULL,
    hash_algorithm      text        NOT NULL DEFAULT 'argon2id',
    scopes              text[]      NOT NULL DEFAULT '{}',
    last_used_at        timestamptz,
    expires_at          timestamptz,
    revoked_at          timestamptz,
    created_by_user_id  uuid,
    revoked_by_user_id  uuid,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_api_keys_org        FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_api_keys_created_by FOREIGN KEY (created_by_user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT fk_api_keys_revoked_by FOREIGN KEY (revoked_by_user_id) REFERENCES users (id) ON DELETE SET NULL,

    CONSTRAINT chk_api_keys_name_len CHECK (char_length(name) BETWEEN 1 AND 80),
    CONSTRAINT uq_api_keys_prefix   UNIQUE (key_prefix),
    CONSTRAINT uq_api_keys_org_id   UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX uq_api_keys_org_name
    ON api_keys (organization_id, name)
    WHERE revoked_at IS NULL;

CREATE INDEX idx_api_keys_org_live
    ON api_keys (organization_id)
    WHERE revoked_at IS NULL;
