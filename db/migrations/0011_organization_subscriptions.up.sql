-- Migration unit 11: organization_subscriptions (database-schema.md §4.11, §7).
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE organization_subscriptions (
    id                   uuid        PRIMARY KEY,
    organization_id      uuid        NOT NULL,
    plan                 text        NOT NULL,
    status               text        NOT NULL,
    current_period_start timestamptz,
    current_period_end   timestamptz,
    trial_ends_at        timestamptz,
    canceled_at          timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_org_subs_org FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE CASCADE,

    CONSTRAINT chk_org_subs_plan CHECK (plan IN ('free', 'pro', 'enterprise')),
    CONSTRAINT chk_org_subs_status CHECK (
        status IN ('trialing', 'active', 'past_due', 'canceled', 'incomplete')
    ),
    CONSTRAINT chk_org_subs_period CHECK (
        current_period_end IS NULL OR current_period_end > current_period_start
    ),
    CONSTRAINT uq_org_subs_org_id UNIQUE (organization_id, id)
);

CREATE UNIQUE INDEX uq_org_subs_open
    ON organization_subscriptions (organization_id)
    WHERE status IN ('trialing', 'active', 'past_due', 'incomplete');

CREATE INDEX idx_org_subs_org
    ON organization_subscriptions (organization_id, created_at DESC);
