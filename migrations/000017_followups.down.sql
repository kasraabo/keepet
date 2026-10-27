DO $$ BEGIN RAISE EXCEPTION 'care followup rollback requires coordinated application and database restore'; END $$;
