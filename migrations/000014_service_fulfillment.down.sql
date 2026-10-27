-- Restoring old settlement triggers over new evidence requires an explicit data migration.
DO $$ BEGIN RAISE EXCEPTION 'fulfillment rollback requires planned restoration of financial triggers and service evidence'; END $$;
