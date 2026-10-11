-- Preserve bookings: a rollback cannot silently delete custom windows in use.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM availability WHERE period='custom') THEN
  RAISE EXCEPTION 'Custom availability exists; reconcile its bookings before rolling back';
 END IF;
END $$;
DROP TRIGGER availability_set_window ON availability;
DROP FUNCTION availability_window_defaults();
DROP INDEX availability_standard_period;
DROP INDEX availability_unique_window;
ALTER TABLE availability DROP CONSTRAINT availability_valid_period;
ALTER TABLE availability DROP CONSTRAINT availability_valid_window;
ALTER TABLE availability DROP COLUMN start_time;
ALTER TABLE availability DROP COLUMN end_time;
ALTER TABLE availability ADD CONSTRAINT availability_provider_id_day_period_key UNIQUE(provider_id,day,period);
