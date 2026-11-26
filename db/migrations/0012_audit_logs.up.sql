-- Migration unit 12: audit_logs (database-schema.md §4.12, §7).
-- actor_id and target_id are intentionally polymorphic and have no FK.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE audit_logs (
    id              uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    actor_type      text        NOT NULL,
    actor_id        uuid,
    action          text        NOT NULL,
    target_type     text,
    target_id       uuid,
    ip              inet,
    user_agent      text,
    metadata        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    occurred_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_audit_org FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE CASCADE,

    CONSTRAINT chk_audit_actor_type CHECK (
        actor_type IN ('user', 'api_key', 'system', 'service')
    ),
    CONSTRAINT chk_audit_action_len CHECK (
        char_length(action) BETWEEN 1 AND 120
    ),
    CONSTRAINT chk_audit_target_type_len CHECK (
        target_type IS NULL OR char_length(target_type) BETWEEN 1 AND 60
    ),
    CONSTRAINT chk_audit_user_agent_len CHECK (
        user_agent IS NULL OR char_length(user_agent) <= 512
    )
);

CREATE INDEX idx_audit_org_time ON audit_logs (organization_id, occurred_at DESC);
CREATE INDEX idx_audit_target   ON audit_logs (target_type, target_id, occurred_at DESC);
CREATE INDEX idx_audit_action   ON audit_logs (action, occurred_at DESC);
