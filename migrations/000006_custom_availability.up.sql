ALTER TABLE availability ADD COLUMN start_time time;
ALTER TABLE availability ADD COLUMN end_time time;
UPDATE availability SET
 start_time=CASE period WHEN 'morning' THEN '09:00'::time WHEN 'noon' THEN '12:00'::time WHEN 'afternoon' THEN '15:00'::time WHEN 'night' THEN '18:00'::time END,
 end_time=CASE period WHEN 'morning' THEN '12:00'::time WHEN 'noon' THEN '15:00'::time WHEN 'afternoon' THEN '18:00'::time WHEN 'night' THEN '21:00'::time END;
CREATE FUNCTION availability_window_defaults() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.period IN ('morning','noon','afternoon','night') THEN
  NEW.start_time := CASE NEW.period WHEN 'morning' THEN '09:00'::time WHEN 'noon' THEN '12:00'::time WHEN 'afternoon' THEN '15:00'::time WHEN 'night' THEN '18:00'::time END;
  NEW.end_time := NEW.start_time+interval '3 hours';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER availability_set_window BEFORE INSERT OR UPDATE ON availability FOR EACH ROW EXECUTE FUNCTION availability_window_defaults();
ALTER TABLE availability ALTER COLUMN start_time SET NOT NULL;
ALTER TABLE availability ALTER COLUMN end_time SET NOT NULL;
ALTER TABLE availability ADD CONSTRAINT availability_valid_window CHECK(start_time<end_time);
ALTER TABLE availability ADD CONSTRAINT availability_valid_period CHECK(period IN ('morning','noon','afternoon','night','custom'));
ALTER TABLE availability DROP CONSTRAINT availability_provider_id_day_period_key;
CREATE UNIQUE INDEX availability_standard_period ON availability(provider_id,day,period) WHERE period<>'custom';
CREATE UNIQUE INDEX availability_unique_window ON availability(provider_id,day,start_time,end_time);
