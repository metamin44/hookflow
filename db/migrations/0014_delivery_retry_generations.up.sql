-- Migration unit 14: manual delivery retry generations (Phase 10).
-- A retry preserves the original Event, Delivery and Attempts and creates a new
-- Delivery identity in the same (event_id, endpoint_id) lineage. Existing rows
-- are the immutable generation 1 with no parent.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

ALTER TABLE deliveries
    ADD COLUMN generation integer NOT NULL DEFAULT 1,
    ADD COLUMN parent_delivery_id uuid;

ALTER TABLE deliveries
    DROP CONSTRAINT uq_deliveries_event_endpoint;

ALTER TABLE deliveries
    ADD CONSTRAINT chk_deliveries_generation CHECK (generation >= 1),
    ADD CONSTRAINT chk_deliveries_lineage CHECK (
        (generation = 1 AND parent_delivery_id IS NULL)
        OR (generation > 1 AND parent_delivery_id IS NOT NULL)
    ),
    ADD CONSTRAINT fk_deliveries_org_parent FOREIGN KEY (organization_id, parent_delivery_id)
        REFERENCES deliveries (organization_id, id) ON DELETE RESTRICT,
    ADD CONSTRAINT uq_deliveries_event_endpoint_generation
        UNIQUE (event_id, endpoint_id, generation);

CREATE INDEX idx_deliveries_parent
    ON deliveries (parent_delivery_id)
    WHERE parent_delivery_id IS NOT NULL;

-- Caller-facing idempotency for POST /v1/deliveries/{delivery_id}/retry. It is
-- separate from ingestion idempotency: a retry key owns a source/new Delivery
-- pair, not an Event, and its 72-hour window is independent of event retention.
CREATE TABLE delivery_retry_requests (
    id                  uuid        PRIMARY KEY,
    organization_id     uuid        NOT NULL,
    idempotency_key     text        NOT NULL,
    request_hash        bytea       NOT NULL,
    source_delivery_id  uuid        NOT NULL,
    new_delivery_id     uuid        NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    expires_at          timestamptz NOT NULL,

    CONSTRAINT fk_retry_org FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_retry_org_source FOREIGN KEY (organization_id, source_delivery_id)
        REFERENCES deliveries (organization_id, id) ON DELETE CASCADE,
    CONSTRAINT fk_retry_org_new FOREIGN KEY (organization_id, new_delivery_id)
        REFERENCES deliveries (organization_id, id) ON DELETE CASCADE,

    CONSTRAINT uq_retry_org_key UNIQUE (organization_id, idempotency_key),

    CONSTRAINT chk_retry_key_len CHECK (
        char_length(idempotency_key) BETWEEN 1 AND 200
    ),
    CONSTRAINT chk_retry_hash_len CHECK (octet_length(request_hash) = 32),
    CONSTRAINT chk_retry_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_retry_distinct CHECK (source_delivery_id <> new_delivery_id)
);

CREATE INDEX idx_retry_expiry ON delivery_retry_requests (expires_at);
CREATE INDEX idx_retry_source ON delivery_retry_requests (source_delivery_id);
CREATE INDEX idx_retry_new ON delivery_retry_requests (new_delivery_id);
