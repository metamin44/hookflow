-- Migration unit 10: outbox_messages (database-schema.md §4.10, §7).
-- available_at has no DEFAULT: every writer derives the gate explicitly.
-- uq_outbox_lease_token gives database-level protection against token reuse; per-row fresh
-- token generation remains an application responsibility.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE outbox_messages (
    id              uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    delivery_id     uuid        NOT NULL,
    sequence        integer     NOT NULL,
    kind            text        NOT NULL,
    topic           text        NOT NULL,
    payload         jsonb       NOT NULL,
    headers         jsonb,
    status          text        NOT NULL DEFAULT 'pending',
    attempts        integer     NOT NULL DEFAULT 0,
    available_at    timestamptz NOT NULL,
    locked_at       timestamptz,
    locked_by       text,
    lease_token     uuid,
    published_at    timestamptz,
    last_error      text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_outbox_org FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_outbox_org_delivery FOREIGN KEY (organization_id, delivery_id)
        REFERENCES deliveries (organization_id, id) ON DELETE CASCADE,

    CONSTRAINT uq_outbox_delivery_sequence UNIQUE (delivery_id, sequence),
    CONSTRAINT uq_outbox_lease_token       UNIQUE (lease_token),

    CONSTRAINT chk_outbox_sequence CHECK (sequence >= 1),
    CONSTRAINT chk_outbox_kind CHECK (kind IN ('delivery.dispatch', 'delivery.retry')),
    CONSTRAINT chk_outbox_status CHECK (
        status IN ('pending', 'publishing', 'published', 'failed')
    ),
    CONSTRAINT chk_outbox_attempts CHECK (attempts >= 0),
    CONSTRAINT chk_outbox_last_error_len CHECK (
        last_error IS NULL OR char_length(last_error) <= 500
    ),
    CONSTRAINT chk_outbox_lease_shape CHECK (
        (
            status = 'publishing'
            AND locked_by IS NOT NULL
            AND locked_at IS NOT NULL
            AND lease_token IS NOT NULL
        )
        OR
        (
            status <> 'publishing'
            AND locked_by IS NULL
            AND locked_at IS NULL
            AND lease_token IS NULL
        )
    )
);

CREATE INDEX idx_outbox_claimable
    ON outbox_messages (available_at, id)
    WHERE status = 'pending';

CREATE INDEX idx_outbox_delivery
    ON outbox_messages (delivery_id, created_at);

CREATE INDEX idx_outbox_org_recent
    ON outbox_messages (organization_id, created_at DESC)
    WHERE status <> 'published';
