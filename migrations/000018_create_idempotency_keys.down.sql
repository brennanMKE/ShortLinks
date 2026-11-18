-- Dropping the table forgets every stored response, so a retry of a request
-- made before the rollback is processed again instead of replayed. Records live
-- at most 24 hours, so the loss is bounded.
DROP TABLE IF EXISTS idempotency_keys;
