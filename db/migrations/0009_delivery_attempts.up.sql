-- Migration unit 9: delivery_attempts (database-schema.md §4.9, §7).
-- U18 write-on-completion: a row exists only for an HTTP execution whose result was
-- observed and durably persisted; finished_at is NOT NULL and there are no start/pending rows.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE delivery_attempts (
    id                    uuid        PRIMARY KEY,
    organization_id       uuid        NOT NULL,
    delivery_id           uuid        NOT NULL,
    attempt_number        integer     NOT NULL,
    request_method        text        NOT NULL DEFAULT 'POST',
    request_url           text        NOT NULL,
    request_headers       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    response_status       integer,
    response_headers      jsonb,
    response_body_snippet text,
    outcome               text        NOT NULL,
    error_class           text,
    error_message         text,
    duration_ms           integer,
    started_at            timestamptz NOT NULL DEFAULT now(),
    finished_at           timestamptz NOT NULL,

    CONSTRAINT fk_attempts_org FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_attempts_org_delivery FOREIGN KEY (organization_id, delivery_id)
        REFERENCES deliveries (organization_id, id) ON DELETE CASCADE,

    CONSTRAINT uq_attempts_delivery_number UNIQUE (delivery_id, attempt_number),

    CONSTRAINT chk_attempts_number CHECK (attempt_number >= 1),
    CONSTRAINT chk_attempts_response_status CHECK (
        response_status IS NULL OR response_status BETWEEN 100 AND 599
    ),
    CONSTRAINT chk_attempts_body_len CHECK (
        response_body_snippet IS NULL OR char_length(response_body_snippet) <= 2000
    ),
    CONSTRAINT chk_attempts_outcome CHECK (
        outcome IN ('success', 'retryable_failure', 'terminal_failure')
    ),
    CONSTRAINT chk_attempts_error_class CHECK (
        error_class IS NULL OR error_class IN ('timeout', 'connect', 'tls', 'dns', 'http_status', 'internal')
    ),
    CONSTRAINT chk_attempts_error_message_len CHECK (
        error_message IS NULL OR char_length(error_message) <= 500
    ),
    CONSTRAINT chk_attempts_duration CHECK (duration_ms IS NULL OR duration_ms >= 0)
);

CREATE INDEX idx_attempts_org_started
    ON delivery_attempts (organization_id, started_at DESC);
