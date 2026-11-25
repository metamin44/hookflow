-- Reverse migration unit 13: idempotency_records.

DROP TRIGGER IF EXISTS trg_idem_linked ON idempotency_records;
DROP FUNCTION IF EXISTS idem_assert_linked();
DROP TABLE idempotency_records;
