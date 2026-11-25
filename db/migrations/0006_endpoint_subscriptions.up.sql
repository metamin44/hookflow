-- Migration unit 6: endpoint_subscriptions (database-schema.md §4.6, §7).
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE endpoint_subscriptions (
    id              uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    endpoint_id     uuid        NOT NULL,
    event_type      text        NOT NULL,
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_es_org FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_es_org_endpoint FOREIGN KEY (organization_id, endpoint_id)
        REFERENCES endpoints (organization_id, id) ON DELETE CASCADE,

    CONSTRAINT chk_es_event_type CHECK (
        char_length(event_type) BETWEEN 1 AND 160
        AND event_type ~ '^[a-z0-9_.-]+$'
    ),
    CONSTRAINT uq_es_endpoint_event UNIQUE (endpoint_id, event_type),
    CONSTRAINT uq_es_org_id         UNIQUE (organization_id, id)
);

CREATE INDEX idx_es_match
    ON endpoint_subscriptions (organization_id, event_type)
    WHERE is_active;

CREATE INDEX idx_es_endpoint
    ON endpoint_subscriptions (endpoint_id)
    WHERE is_active;
