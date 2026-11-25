-- Migration unit 8: deliveries (database-schema.md §4.8, §7).
-- Snapshot payload fields url/http_headers/secret_encrypted are immutable after acceptance.
-- endpoint_subscription_id is a nullable provenance pointer (F3-C): the composite FK may
-- null only that column without changing the snapshot or cancelling the delivery.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE deliveries (
    id                        uuid        PRIMARY KEY,
    organization_id           uuid        NOT NULL,
    event_id                  uuid        NOT NULL,
    endpoint_id               uuid        NOT NULL,
    endpoint_subscription_id  uuid,
    url                       text        NOT NULL,
    http_headers              jsonb       NOT NULL DEFAULT '{}'::jsonb,
    secret_encrypted          text        NOT NULL,
    state                     text        NOT NULL DEFAULT 'pending',
    dispatch_seq              integer     NOT NULL,
    attempt_count             integer     NOT NULL DEFAULT 0,
    next_attempt_at           timestamptz,
    last_attempt_at           timestamptz,
    last_response_status      integer,
    last_error                text,
    priority                  smallint    NOT NULL DEFAULT 0,
    created_at                timestamptz NOT NULL DEFAULT now(),
    updated_at                timestamptz NOT NULL DEFAULT now(),
    completed_at              timestamptz,
    dispatch_lease_token      uuid,
    dispatch_lease_expires_at timestamptz,

    CONSTRAINT fk_deliveries_org FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_deliveries_org_event FOREIGN KEY (organization_id, event_id)
        REFERENCES events (organization_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_deliveries_org_endpoint FOREIGN KEY (organization_id, endpoint_id)
        REFERENCES endpoints (organization_id, id) ON DELETE RESTRICT,
    CONSTRAINT fk_deliveries_org_endpoint_subscription FOREIGN KEY (organization_id, endpoint_subscription_id)
        REFERENCES endpoint_subscriptions (organization_id, id)
        ON DELETE SET NULL (endpoint_subscription_id),

    CONSTRAINT uq_deliveries_event_endpoint UNIQUE (event_id, endpoint_id),
    CONSTRAINT uq_deliveries_org_id         UNIQUE (organization_id, id),

    CONSTRAINT chk_deliveries_state CHECK (
        state IN ('pending', 'retrying', 'succeeded', 'failed', 'expired')
    ),
    CONSTRAINT chk_deliveries_dispatch_seq CHECK (dispatch_seq >= 1),
    CONSTRAINT chk_deliveries_attempt_count CHECK (attempt_count >= 0),
    CONSTRAINT chk_deliveries_last_response_status CHECK (
        last_response_status IS NULL OR last_response_status BETWEEN 100 AND 599
    ),
    CONSTRAINT chk_deliveries_last_error_len CHECK (
        last_error IS NULL OR char_length(last_error) <= 500
    ),
    CONSTRAINT chk_deliveries_completed CHECK (
        (state IN ('succeeded', 'failed', 'expired')) = (completed_at IS NOT NULL)
    ),
    CONSTRAINT chk_deliveries_worker_lease CHECK (
        (dispatch_lease_token IS NULL) = (dispatch_lease_expires_at IS NULL)
    ),
    CONSTRAINT chk_deliveries_scheduling CHECK (
        (state IN ('pending', 'retrying')) = (next_attempt_at IS NOT NULL)
    )
);

CREATE INDEX idx_deliveries_due
    ON deliveries (next_attempt_at)
    WHERE state IN ('pending', 'retrying');

CREATE INDEX idx_deliveries_org_created
    ON deliveries (organization_id, created_at DESC);

CREATE INDEX idx_deliveries_event
    ON deliveries (event_id);

CREATE INDEX idx_deliveries_endpoint_state
    ON deliveries (endpoint_id, state, created_at DESC);
