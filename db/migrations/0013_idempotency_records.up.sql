-- Migration unit 13: idempotency_records (database-schema.md §4.13, §7).
-- Includes the U44 deferred constraint trigger: an idempotency record may not commit
-- without a linked event_id. The constraint is deferred so event creation and its
-- idempotency record can commit in the same transaction.
-- IDs are application-generated UUIDv7: no UUID DEFAULT or generation function.

CREATE TABLE idempotency_records (
    id               uuid        PRIMARY KEY,
    organization_id  uuid        NOT NULL,
    idempotency_key  text        NOT NULL,
    request_hash     bytea       NOT NULL,
    hash_algorithm   text        NOT NULL DEFAULT 'sha256',
    event_id         uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,

    CONSTRAINT fk_idem_org FOREIGN KEY (organization_id)
        REFERENCES organizations (id) ON DELETE CASCADE,
    CONSTRAINT fk_idem_org_event FOREIGN KEY (organization_id, event_id)
        REFERENCES events (organization_id, id) ON DELETE CASCADE,

    CONSTRAINT uq_idem_org_key UNIQUE (organization_id, idempotency_key),

    CONSTRAINT chk_idem_key_len CHECK (
        char_length(idempotency_key) BETWEEN 1 AND 200
    ),
    CONSTRAINT chk_idem_hash_len CHECK (octet_length(request_hash) = 32),
    CONSTRAINT chk_idem_hash_alg CHECK (hash_algorithm = 'sha256'),
    CONSTRAINT chk_idem_expiry CHECK (expires_at > created_at)
);

CREATE INDEX idx_idem_expiry ON idempotency_records (expires_at);
CREATE INDEX idx_idem_event  ON idempotency_records (event_id);

CREATE OR REPLACE FUNCTION idem_assert_linked() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    current_event_id uuid;
BEGIN
    SELECT event_id
      INTO current_event_id
      FROM public.idempotency_records
     WHERE id = NEW.id;

    IF NOT FOUND THEN
        RETURN NEW;
    END IF;

    IF current_event_id IS NULL THEN
        RAISE EXCEPTION 'idempotency_record % committed without event_id', NEW.id
            USING ERRCODE = 'check_violation';
    END IF;

    RETURN NEW;
END $$;

CREATE CONSTRAINT TRIGGER trg_idem_linked
    AFTER INSERT OR UPDATE ON idempotency_records
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION idem_assert_linked();
