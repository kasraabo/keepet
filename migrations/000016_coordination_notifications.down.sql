DO $$ BEGIN RAISE EXCEPTION 'coordination archive rollback requires coordinated application and database restore'; END $$;
