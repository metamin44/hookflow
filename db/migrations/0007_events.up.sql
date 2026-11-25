-- Migration unit 7: events (database-schema.md §4.7, §7).
-- F3-B: tenant-safe composite provenance edge to api_keys with a column-targeted SET NULL.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE events (
    id                uuid        PRIMARY KEY,
    organization_id   uuid        NOT NULL,
    type              text        NOT NULL,
    payload           jsonb       NOT NULL,
    destination_count integer     NOT NULL DEFAULT 0,
    headers           jsonb,
    api_key_id        uuid,
    source_ip         inet,
    occurred_at       timestamptz,
    received_at       timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_events_org FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_events_org_api_key FOREIGN KEY (organization_id, api_key_id)
        REFERENCES api_keys (organization_id, id) ON DELETE SET NULL (api_key_id),

    CONSTRAINT chk_events_type              CHECK (
        char_length(type) BETWEEN 1 AND 160
        AND type ~ '^[a-z0-9_.-]+$'
    ),
    CONSTRAINT chk_events_destination_count CHECK (destination_count >= 0),
    CONSTRAINT chk_events_expires           CHECK (expires_at > received_at),
    CONSTRAINT uq_events_org_id             UNIQUE (organization_id, id)
);

CREATE INDEX idx_events_org_received ON events (organization_id, received_at DESC);
CREATE INDEX idx_events_org_type     ON events (organization_id, type, received_at DESC);
CREATE INDEX idx_events_expires      ON events (expires_at);
