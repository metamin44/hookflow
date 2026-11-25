-- Migration unit 3: organization_members (database-schema.md §4.3, §7).
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE organization_members (
    id              uuid        PRIMARY KEY,
    organization_id uuid        NOT NULL,
    user_id         uuid        NOT NULL,
    role            text        NOT NULL,
    invited_by      uuid,
    joined_at       timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT fk_members_org        FOREIGN KEY (organization_id) REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_members_user       FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE,
    CONSTRAINT fk_members_invited_by FOREIGN KEY (invited_by) REFERENCES users (id) ON DELETE SET NULL,

    CONSTRAINT chk_members_role CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    CONSTRAINT uq_members_org_user UNIQUE (organization_id, user_id),
    CONSTRAINT uq_members_org_id   UNIQUE (organization_id, id)
);

CREATE INDEX idx_members_user ON organization_members (user_id);
