-- Reverse migration unit 14: manual delivery retry generations.
-- Rolling back would collapse a lineage into a single row and silently destroy
-- retry history, so refuse while any retry generation exists. Operators must
-- resolve that data explicitly before rolling back.

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM deliveries WHERE generation > 1) THEN
        RAISE EXCEPTION 'cannot roll back migration 0014 while delivery retry generations exist'
            USING ERRCODE = 'check_violation';
    END IF;
END $$;

DROP TABLE delivery_retry_requests;
DROP INDEX idx_deliveries_parent;

ALTER TABLE deliveries
    DROP CONSTRAINT uq_deliveries_event_endpoint_generation,
    DROP CONSTRAINT fk_deliveries_org_parent,
    DROP CONSTRAINT chk_deliveries_lineage,
    DROP CONSTRAINT chk_deliveries_generation;

ALTER TABLE deliveries
    DROP COLUMN parent_delivery_id,
    DROP COLUMN generation;

ALTER TABLE deliveries
    ADD CONSTRAINT uq_deliveries_event_endpoint UNIQUE (event_id, endpoint_id);
