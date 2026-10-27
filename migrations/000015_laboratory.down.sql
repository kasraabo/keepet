DO $$ BEGIN RAISE EXCEPTION 'laboratory rollback requires a coordinated database backup and application restore'; END $$;
