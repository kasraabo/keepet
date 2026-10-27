ALTER TABLE providers ADD COLUMN requires_ops_approval boolean NOT NULL DEFAULT false, ADD COLUMN approval_revision bigint NOT NULL DEFAULT 1;
CREATE TABLE booking_approval_settings(id integer PRIMARY KEY CHECK(id=1),revision bigint NOT NULL DEFAULT 1,ops_minutes integer NOT NULL DEFAULT 30 CHECK(ops_minutes BETWEEN 1 AND 1440),provider_minutes integer NOT NULL DEFAULT 30 CHECK(provider_minutes BETWEEN 1 AND 1440),payment_minutes integer NOT NULL DEFAULT 15 CHECK(payment_minutes BETWEEN 1 AND 1440),reminder_minutes integer NOT NULL DEFAULT 10 CHECK(reminder_minutes BETWEEN 1 AND 1440));
INSERT INTO booking_approval_settings(id) VALUES(1);
ALTER TABLE bookings ADD COLUMN request_flow boolean NOT NULL DEFAULT false, ADD COLUMN requires_ops_approval boolean NOT NULL DEFAULT false, ADD COLUMN approval_policy jsonb, ADD COLUMN submitted_at timestamptz, ADD COLUMN stage_deadline timestamptz, ADD COLUMN provider_requested_at timestamptz, ADD COLUMN request_reason text NOT NULL DEFAULT '';
CREATE INDEX booking_approval_pending ON bookings(stage_deadline) WHERE request_flow AND status IN ('pending_ops','pending_provider','awaiting_payment');
CREATE FUNCTION booking_holds_capacity(p_payment text,p_status text,p_deadline timestamptz) RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT (p_payment='paid' AND p_status NOT IN ('cancelled','cancel_requested')) OR (p_status='awaiting_payment' AND p_deadline>now()) $$;
CREATE FUNCTION booking_request_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.request_flow AND OLD.submitted_at IS NOT NULL THEN
  IF NEW.amount IS DISTINCT FROM OLD.amount OR NEW.provider_due IS DISTINCT FROM OLD.provider_due OR NEW.pricing_snapshot IS DISTINCT FROM OLD.pricing_snapshot OR NEW.commission_bps IS DISTINCT FROM OLD.commission_bps OR NEW.approval_policy IS DISTINCT FROM OLD.approval_policy OR NEW.requires_ops_approval IS DISTINCT FROM OLD.requires_ops_approval OR NEW.provider_id IS DISTINCT FROM OLD.provider_id OR NEW.availability_id IS DISTINCT FROM OLD.availability_id THEN RAISE EXCEPTION 'submitted request agreement is immutable'; END IF;
  IF OLD.payment_status<>'paid' AND NEW.payment_status='paid' AND (OLD.status<>'awaiting_payment' OR OLD.stage_deadline<=now()) THEN RAISE EXCEPTION 'request is not ready for payment'; END IF;
 END IF; RETURN NEW;
END $$;
CREATE TRIGGER booking_request_guard BEFORE UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION booking_request_guard();
