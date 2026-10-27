CREATE TABLE fulfillment_settings(id integer PRIMARY KEY CHECK(id=1),revision bigint NOT NULL DEFAULT 1,arrival_minutes integer NOT NULL DEFAULT 10 CHECK(arrival_minutes BETWEEN 1 AND 60),arrival_attempts integer NOT NULL DEFAULT 5 CHECK(arrival_attempts BETWEEN 1 AND 10),resend_seconds integer NOT NULL DEFAULT 30 CHECK(resend_seconds BETWEEN 15 AND 600),hourly_limit integer NOT NULL DEFAULT 6 CHECK(hourly_limit BETWEEN 1 AND 20),completion_hours integer NOT NULL DEFAULT 24 CHECK(completion_hours BETWEEN 1 AND 168));
INSERT INTO fulfillment_settings(id) VALUES(1);
ALTER TABLE bookings ADD COLUMN fulfillment_flow boolean NOT NULL DEFAULT false,ADD COLUMN attendance_verified_at timestamptz,ADD COLUMN completion_reported_at timestamptz,ADD COLUMN completion_deadline timestamptz,ADD COLUMN completion_hours integer NOT NULL DEFAULT 24 CHECK(completion_hours BETWEEN 1 AND 168),ADD COLUMN fulfillment_disputed boolean NOT NULL DEFAULT false,ADD COLUMN fulfillment_issue text NOT NULL DEFAULT '',ADD COLUMN settlement_approved_at timestamptz,ADD COLUMN receipt_approved_at timestamptz,ADD COLUMN receipt_approved_by text REFERENCES users(id);
ALTER TABLE bookings ADD COLUMN completion_method text NOT NULL DEFAULT '',ADD COLUMN completion_confirmed_by text REFERENCES users(id);
CREATE TABLE arrival_challenges(booking_id text PRIMARY KEY REFERENCES bookings(id),challenge_id text NOT NULL,code_hash text NOT NULL,code_cipher bytea NOT NULL,expires_at timestamptz NOT NULL,attempts integer NOT NULL DEFAULT 0,max_attempts integer NOT NULL,consumed_at timestamptz,last_sent_at timestamptz NOT NULL DEFAULT now(),hour_started_at timestamptz NOT NULL DEFAULT now(),send_count integer NOT NULL DEFAULT 1);
CREATE FUNCTION arrival_consumed_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.consumed_at IS NOT NULL THEN RAISE EXCEPTION 'consumed arrival challenge is immutable'; END IF;
 IF NEW.booking_id<>OLD.booking_id THEN RAISE EXCEPTION 'arrival booking is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER arrival_consumed_immutable BEFORE UPDATE ON arrival_challenges FOR EACH ROW EXECUTE FUNCTION arrival_consumed_immutable();
CREATE TABLE handover_receipts(id text PRIMARY KEY,booking_id text NOT NULL REFERENCES bookings(id),submitted_by text NOT NULL REFERENCES users(id),lab_name text NOT NULL,reference text NOT NULL DEFAULT '',filename text NOT NULL,mime text NOT NULL,data bytea NOT NULL CHECK(octet_length(data) BETWEEN 1 AND 8388608),digest text NOT NULL,state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','rejected','approved')),reason text NOT NULL DEFAULT '',reviewed_by text REFERENCES users(id),reviewed_at timestamptz,created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX handover_receipts_booking ON handover_receipts(booking_id,created_at DESC);
ALTER TABLE bookings ADD COLUMN handover_receipt_id text REFERENCES handover_receipts(id);
CREATE INDEX fulfillment_pending ON bookings(completion_deadline) WHERE fulfillment_flow AND status='awaiting_customer' AND NOT fulfillment_disputed;
CREATE FUNCTION handover_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.id<>OLD.id OR NEW.booking_id<>OLD.booking_id OR NEW.submitted_by<>OLD.submitted_by OR NEW.lab_name<>OLD.lab_name OR NEW.reference<>OLD.reference OR NEW.filename<>OLD.filename OR NEW.mime<>OLD.mime OR NEW.data<>OLD.data OR NEW.digest<>OLD.digest OR NEW.created_at<>OLD.created_at OR OLD.state<>'pending' OR NEW.state NOT IN ('approved','rejected') OR NOT EXISTS(SELECT 1 FROM users WHERE id=NEW.reviewed_by AND role IN ('ops','admin')) THEN RAISE EXCEPTION 'handover receipt is immutable'; END IF;RETURN NEW;
END $$;
CREATE TRIGGER handover_immutable BEFORE UPDATE ON handover_receipts FOR EACH ROW EXECUTE FUNCTION handover_immutable();
CREATE FUNCTION booking_settlement_ready(b bookings) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT COALESCE((CASE WHEN NOT b.fulfillment_flow THEN b.status IN ('completed','result_ready')
 WHEN b.service='vet' THEN b.status='completed' AND EXISTS(SELECT 1 FROM consultation_chats c JOIN consultation_vet_forms f ON f.chat_id=c.id WHERE c.booking_id=b.id AND trim(f.symptoms)<>'' AND trim(f.diagnosis)<>'' AND trim(f.prescription)<>'')
 WHEN b.service='grooming' THEN b.status='completed' AND b.attendance_verified_at IS NOT NULL AND b.settlement_approved_at IS NOT NULL AND b.completion_reported_at IS NOT NULL AND NOT b.fulfillment_disputed AND ((b.completion_method='customer' AND b.completion_confirmed_by=b.owner_id) OR (b.completion_method='automatic' AND b.completion_deadline<=now()))
 WHEN b.service='sampling' THEN b.status IN ('delivered','result_ready') AND b.attendance_verified_at IS NOT NULL AND b.receipt_approved_at IS NOT NULL AND b.settlement_approved_at IS NOT NULL AND NOT b.fulfillment_disputed AND EXISTS(SELECT 1 FROM handover_receipts r WHERE r.id=b.handover_receipt_id AND r.booking_id=b.id AND r.state='approved' AND r.reviewed_by=b.receipt_approved_by AND r.reviewed_at=b.receipt_approved_at)
 ELSE false END),false)
$$;
CREATE FUNCTION booking_referral_ready(b bookings) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN b.fulfillment_flow THEN booking_settlement_ready(b) ELSE ((b.service='sampling' AND b.status='result_ready') OR (b.service<>'sampling' AND b.status='completed')) END
$$;
CREATE FUNCTION fulfillment_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT OLD.fulfillment_flow THEN RETURN NEW; END IF;
 IF NOT NEW.fulfillment_flow THEN
  IF OLD.status='draft' AND OLD.submitted_at IS NULL AND OLD.payment_status<>'paid' THEN RETURN NEW; END IF;
  RAISE EXCEPTION 'fulfillment cannot be disabled';
 END IF;
 IF OLD.submitted_at IS NOT NULL AND NEW.completion_hours<>OLD.completion_hours THEN RAISE EXCEPTION 'completion policy is immutable'; END IF;
 IF OLD.attendance_verified_at IS NULL AND NEW.attendance_verified_at IS NOT NULL AND (NEW.payment_status<>'paid' OR NOT EXISTS(SELECT 1 FROM arrival_challenges c WHERE c.booking_id=NEW.id AND c.consumed_at=NEW.attendance_verified_at)) THEN RAISE EXCEPTION 'consumed arrival challenge required'; END IF;
 IF OLD.attendance_verified_at IS NOT NULL AND NEW.attendance_verified_at IS DISTINCT FROM OLD.attendance_verified_at THEN RAISE EXCEPTION 'attendance is immutable'; END IF;
 IF OLD.settlement_approved_at IS NOT NULL AND (NEW.settlement_approved_at IS DISTINCT FROM OLD.settlement_approved_at OR NEW.attendance_verified_at IS DISTINCT FROM OLD.attendance_verified_at OR NEW.receipt_approved_at IS DISTINCT FROM OLD.receipt_approved_at OR NEW.handover_receipt_id IS DISTINCT FROM OLD.handover_receipt_id OR NEW.fulfillment_disputed IS DISTINCT FROM OLD.fulfillment_disputed OR NEW.completion_reported_at IS DISTINCT FROM OLD.completion_reported_at OR NEW.completion_deadline IS DISTINCT FROM OLD.completion_deadline OR NEW.receipt_approved_by IS DISTINCT FROM OLD.receipt_approved_by OR NEW.completion_method IS DISTINCT FROM OLD.completion_method OR NEW.completion_confirmed_by IS DISTINCT FROM OLD.completion_confirmed_by) THEN RAISE EXCEPTION 'settled service evidence is immutable'; END IF;
 IF NEW.service IN ('grooming','sampling') AND NEW.status IN ('in_progress','awaiting_customer','collected','delivery_pending','delivered','completed','result_ready') AND NEW.attendance_verified_at IS NULL THEN RAISE EXCEPTION 'verified attendance required'; END IF;
 IF NEW.service='sampling' AND NEW.status IN ('delivered','result_ready') AND (NEW.receipt_approved_at IS NULL OR NOT EXISTS(SELECT 1 FROM handover_receipts r WHERE r.id=NEW.handover_receipt_id AND r.booking_id=NEW.id AND r.state='approved')) THEN RAISE EXCEPTION 'approved handover required'; END IF;
 IF NEW.status='completed' AND NOT booking_settlement_ready(NEW) THEN RAISE EXCEPTION 'service not ready to settle'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER fulfillment_guard BEFORE UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION fulfillment_guard();

CREATE OR REPLACE FUNCTION wallet_booking_credit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE uid text; due bigint; bonus_refund bigint;
BEGIN
 IF NEW.payment_status='paid' AND booking_settlement_ready(NEW) AND NEW.refund_status='' AND NEW.provider_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM business_payouts WHERE booking_id=NEW.id) THEN
  SELECT user_id INTO uid FROM providers WHERE id=NEW.provider_id;
  due:=COALESCE(NEW.provider_due,NEW.amount-NEW.amount*NEW.commission_bps/10000);
  IF uid IS NOT NULL AND due>0 THEN PERFORM wallet_post(uid,'service_income',due,0,0,'income:'||NEW.id,NEW.id,NULL,'سهم خدمت تکمیل‌شده'); END IF;
 END IF;
 IF NEW.refund_status='refunded' AND OLD.refund_status IS DISTINCT FROM 'refunded' AND NEW.owner_id IS NOT NULL AND NEW.refund_amount>0 THEN
  bonus_refund:=LEAST(NEW.refund_amount,NEW.wallet_bonus_used);
  PERFORM wallet_post(NEW.owner_id,'refund',NEW.refund_amount-bonus_refund,bonus_refund,0,'refund:'||NEW.id,NEW.id,NULL,'بازگشت وجه رزرو به کیف پول');
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION referral_booking_locks() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ids text[]; uid text;
BEGIN
 IF NEW.payment_status='paid' AND (booking_settlement_ready(NEW) OR NEW.refund_status='refunded') THEN
  SELECT array_agg(DISTINCT x ORDER BY x) INTO ids FROM (
   SELECT NEW.owner_id x UNION SELECT user_id FROM providers WHERE id=NEW.provider_id UNION SELECT referrer_id FROM referral_order_awards WHERE booking_id=NEW.id UNION SELECT r.referrer_id FROM referrals r JOIN providers p ON p.user_id=r.invitee_id WHERE p.id=NEW.provider_id
  ) p WHERE x IS NOT NULL;
  FOREACH uid IN ARRAY COALESCE(ids,ARRAY[]::text[]) LOOP
   INSERT INTO wallets(user_id) VALUES(uid) ON CONFLICT DO NOTHING;
   PERFORM user_id FROM wallets WHERE user_id=uid FOR UPDATE;
  END LOOP;
 END IF; RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION referral_booking_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE a referral_order_awards; target bigint; reversal bigint;
BEGIN
 IF NEW.payment_status<>'paid' THEN RETURN NEW; END IF;
 IF OLD.payment_status<>'paid' THEN PERFORM referral_refresh_order(NEW.id);
 ELSIF NEW.provider_id IS DISTINCT FROM OLD.provider_id AND EXISTS(SELECT 1 FROM referral_booking_policy WHERE booking_id=NEW.id) THEN PERFORM referral_refresh_order(NEW.id); END IF;
 SELECT * INTO a FROM referral_order_awards WHERE booking_id=NEW.id FOR UPDATE; IF NOT FOUND THEN RETURN NEW; END IF;
 IF NEW.refund_status='refunded' THEN
  IF a.status='pending' THEN UPDATE referral_order_awards SET status='cancelled' WHERE booking_id=NEW.id;
  ELSIF a.status='credited' THEN
   target:=LEAST(a.credited,floor(a.amount::numeric*LEAST(NEW.amount,GREATEST(0,NEW.refund_amount))/GREATEST(1,NEW.amount))::bigint);reversal:=GREATEST(0,target-a.adjusted);
   IF reversal>0 THEN
    INSERT INTO referral_accounts(user_id,debt) VALUES(a.referrer_id,reversal) ON CONFLICT(user_id) DO UPDATE SET debt=referral_accounts.debt+EXCLUDED.debt;
    INSERT INTO referral_events(user_id,invitee_id,booking_id,kind,amount,debt_delta,event_key) VALUES(a.referrer_id,a.provider_user_id,NEW.id,'order_reversal',reversal,reversal,'reversal:'||NEW.id||':'||target);
    UPDATE referral_order_awards SET adjusted=adjusted+reversal WHERE booking_id=NEW.id;
    PERFORM referral_sweep(a.referrer_id);
   END IF;
  END IF;
 ELSIF booking_referral_ready(NEW) AND a.status='pending' AND a.amount=0 THEN
  UPDATE referral_order_awards SET status='cancelled' WHERE booking_id=NEW.id;
 ELSIF booking_referral_ready(NEW) AND NEW.refund_status='' AND a.status='pending' AND a.amount>0 AND NOT EXISTS(SELECT 1 FROM business_payouts WHERE booking_id=NEW.id) THEN
  INSERT INTO referral_events(user_id,invitee_id,booking_id,kind,amount,cash_delta,event_key) VALUES(a.referrer_id,a.provider_user_id,NEW.id,'order_income',a.amount,a.amount,'income:'||NEW.id);
  PERFORM wallet_post(a.referrer_id,'referral_income',a.amount,0,0,'referral:income:'||NEW.id,NEW.id,NULL,'سهم معرف از کمیسیون خدمت تکمیل‌شده');
  UPDATE referral_order_awards SET credited=amount,status='credited',credited_at=now() WHERE booking_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
