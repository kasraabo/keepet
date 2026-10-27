ALTER TABLE provider_tariffs ADD COLUMN duration_minutes integer CHECK(duration_minutes BETWEEN 5 AND 480);
ALTER TABLE bookings ADD COLUMN time_flow boolean NOT NULL DEFAULT false,ADD COLUMN duration_minutes integer CHECK(duration_minutes BETWEEN 5 AND 1440);
UPDATE business_config SET data=jsonb_set(data,'{duration_minutes}','30') WHERE (kind='grooming' OR (kind='service' AND id='sampling')) AND COALESCE((data->>'duration_minutes')::integer,0)=0;
CREATE INDEX bookings_provider_reservations ON bookings(provider_id,exact_time) WHERE payment_status='paid' OR status='awaiting_payment';
-- Historical requests with unknown duration reserve their entire registered window.
CREATE FUNCTION provider_reservations(p_provider text,p_exclude text) RETURNS TABLE(starts timestamptz,ends timestamptz) LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN b.time_flow THEN b.exact_time ELSE COALESCE(b.exact_time,(a.day+a.start_time) AT TIME ZONE 'Asia/Tehran',date_trunc('day',b.exact_time AT TIME ZONE 'Asia/Tehran') AT TIME ZONE 'Asia/Tehran') END,
 CASE WHEN b.time_flow THEN b.exact_time+b.duration_minutes*interval '1 minute' ELSE COALESCE((a.day+a.end_time) AT TIME ZONE 'Asia/Tehran',(date_trunc('day',b.exact_time AT TIME ZONE 'Asia/Tehran')+interval '1 day') AT TIME ZONE 'Asia/Tehran') END
 FROM bookings b LEFT JOIN availability a ON a.id=b.availability_id WHERE b.provider_id=p_provider AND b.id<>p_exclude AND booking_holds_capacity(b.payment_status,b.status,b.stage_deadline)
$$;
CREATE FUNCTION booking_window_peak(p_window text,p_exclude text) RETURNS integer LANGUAGE sql STABLE AS $$
 WITH w AS (SELECT provider_id,(day+start_time) AT TIME ZONE 'Asia/Tehran' s,(day+end_time) AT TIME ZONE 'Asia/Tehran' e FROM availability WHERE id=p_window),r AS (SELECT x.* FROM w CROSS JOIN LATERAL provider_reservations(w.provider_id,p_exclude) x WHERE x.starts<w.e AND x.ends>w.s),points AS (SELECT s t FROM w UNION SELECT starts FROM r,w WHERE starts>=w.s AND starts<w.e)
 SELECT COALESCE(max((SELECT count(*) FROM r WHERE r.starts<=points.t AND r.ends>points.t)),0)::integer FROM points
$$;
CREATE FUNCTION booking_time_available(p_provider text,p_window text,p_start timestamptz,p_minutes integer,p_exclude text) RETURNS boolean LANGUAGE sql STABLE AS $$
 WITH w AS (SELECT * FROM availability WHERE id=p_window AND provider_id=p_provider),span AS (SELECT p_start s,p_start+p_minutes*interval '1 minute' e),r AS (SELECT x.* FROM span CROSS JOIN LATERAL provider_reservations(p_provider,p_exclude) x WHERE x.starts<span.e AND x.ends>span.s),windows AS (SELECT (day+start_time) AT TIME ZONE 'Asia/Tehran' s,(day+end_time) AT TIME ZONE 'Asia/Tehran' e,capacity FROM availability WHERE provider_id=p_provider AND day=(p_start AT TIME ZONE 'Asia/Tehran')::date),points AS (SELECT p_start t UNION SELECT starts FROM r,span WHERE starts>=span.s AND starts<span.e UNION SELECT windows.s FROM windows,span WHERE windows.s>span.s AND windows.s<span.e UNION SELECT windows.e FROM windows,span WHERE windows.e>span.s AND windows.e<span.e)
 SELECT COALESCE((SELECT p_minutes BETWEEN 5 AND 1440 AND p_start>now() AND span.s>=(w.day+w.start_time) AT TIME ZONE 'Asia/Tehran' AND span.e<=(w.day+w.end_time) AT TIME ZONE 'Asia/Tehran' AND NOT EXISTS(SELECT 1 FROM points WHERE (SELECT count(*) FROM r WHERE r.starts<=points.t AND r.ends>points.t)>=LEAST(w.capacity,COALESCE((SELECT min(capacity) FROM windows WHERE windows.s<=points.t AND windows.e>points.t),w.capacity))) FROM w,span),false)
$$;
CREATE FUNCTION booking_window_has_time(p_window text,p_minutes integer,p_exclude text) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM availability a CROSS JOIN LATERAL generate_series((a.day+a.start_time) AT TIME ZONE 'Asia/Tehran',((a.day+a.end_time) AT TIME ZONE 'Asia/Tehran')-p_minutes*interval '1 minute',interval '15 minutes') t WHERE a.id=p_window AND booking_time_available(a.provider_id,a.id,t,p_minutes,p_exclude))
$$;
CREATE FUNCTION exact_booking_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.time_flow AND OLD.submitted_at IS NOT NULL AND (NEW.exact_time IS DISTINCT FROM OLD.exact_time OR NEW.duration_minutes IS DISTINCT FROM OLD.duration_minutes OR NEW.time_flow IS DISTINCT FROM OLD.time_flow) THEN RAISE EXCEPTION 'submitted appointment time is immutable'; END IF;
 IF NEW.time_flow AND booking_holds_capacity(NEW.payment_status,NEW.status,NEW.stage_deadline) AND (NOT booking_holds_capacity(OLD.payment_status,OLD.status,OLD.stage_deadline) OR NEW.exact_time IS DISTINCT FROM OLD.exact_time OR NEW.duration_minutes IS DISTINCT FROM OLD.duration_minutes OR NEW.payment_status IS DISTINCT FROM OLD.payment_status) THEN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.provider_id,13013));
  IF NOT booking_time_available(NEW.provider_id,NEW.availability_id,NEW.exact_time,NEW.duration_minutes,NEW.id) THEN RAISE EXCEPTION 'appointment capacity unavailable'; END IF;
 END IF; RETURN NEW;
END $$;
CREATE TRIGGER exact_booking_guard BEFORE UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION exact_booking_guard();
