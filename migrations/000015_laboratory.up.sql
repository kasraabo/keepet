CREATE TABLE lab_catalog(id integer PRIMARY KEY CHECK(id=1),revision bigint NOT NULL DEFAULT 1);
INSERT INTO lab_catalog(id) VALUES(1);
CREATE TABLE laboratories(id text PRIMARY KEY,name text NOT NULL,address text NOT NULL DEFAULT '',active boolean NOT NULL DEFAULT true,revision bigint NOT NULL DEFAULT 1);
CREATE TABLE lab_tests(id text PRIMARY KEY,lab_id text NOT NULL REFERENCES laboratories(id),name text NOT NULL,sample text NOT NULL CHECK(sample IN ('blood','urine','stool')),cost bigint NOT NULL CHECK(cost BETWEEN 0 AND 1000000000),active boolean NOT NULL DEFAULT true,revision bigint NOT NULL DEFAULT 1);
CREATE TABLE lab_packages(id text PRIMARY KEY,name text NOT NULL,active boolean NOT NULL DEFAULT true,revision bigint NOT NULL DEFAULT 1);
CREATE TABLE lab_package_items(package_id text REFERENCES lab_packages(id),test_id text REFERENCES lab_tests(id),PRIMARY KEY(package_id,test_id));
ALTER TABLE providers ADD COLUMN sampling_rate bigint CHECK(sampling_rate BETWEEN 1 AND 1000000000);
ALTER TABLE bookings ADD COLUMN lab_flow boolean NOT NULL DEFAULT false,ADD COLUMN lab_cost bigint NOT NULL DEFAULT 0 CHECK(lab_cost BETWEEN 0 AND 10000000000);
CREATE TABLE lab_reports(id text PRIMARY KEY,booking_id text NOT NULL REFERENCES bookings(id),body text NOT NULL DEFAULT '',filename text NOT NULL DEFAULT '',mime text NOT NULL DEFAULT '',data bytea CHECK(octet_length(data) BETWEEN 1 AND 8388608),uploaded_by text NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now(),state text NOT NULL DEFAULT 'draft' CHECK(state IN ('draft','published')),published_by text REFERENCES users(id),published_at timestamptz);
CREATE INDEX lab_reports_booking ON lab_reports(booking_id,created_at DESC);
ALTER TABLE bookings ADD COLUMN lab_report_id text REFERENCES lab_reports(id);
CREATE TABLE lab_payments(booking_id text PRIMARY KEY REFERENCES bookings(id),amount bigint NOT NULL CHECK(amount>0),reference text NOT NULL,actor_id text NOT NULL REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now());
CREATE FUNCTION lab_payment_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM bookings b WHERE b.id=NEW.booking_id AND b.lab_flow AND b.lab_cost=NEW.amount AND b.payment_status='paid' AND b.refund_status='' AND b.settlement_approved_at IS NOT NULL) OR NOT EXISTS(SELECT 1 FROM users WHERE id=NEW.actor_id AND role IN ('ops','admin')) THEN RAISE EXCEPTION 'valid laboratory payable required'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER lab_payment_guard BEFORE INSERT ON lab_payments FOR EACH ROW EXECUTE FUNCTION lab_payment_guard();
CREATE TRIGGER lab_payments_immutable BEFORE UPDATE OR DELETE ON lab_payments FOR EACH ROW EXECUTE FUNCTION wallet_immutable();
CREATE FUNCTION lab_report_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'report history is immutable'; END IF;
 IF OLD.state<>'draft' OR NEW.state<>'published' OR NEW.id<>OLD.id OR NEW.booking_id<>OLD.booking_id OR NEW.body<>OLD.body OR NEW.filename<>OLD.filename OR NEW.mime<>OLD.mime OR NEW.data IS DISTINCT FROM OLD.data OR NEW.uploaded_by<>OLD.uploaded_by OR NEW.created_at<>OLD.created_at OR NEW.published_at IS NULL OR NOT EXISTS(SELECT 1 FROM users WHERE id=NEW.published_by AND role IN ('admin','ops')) THEN RAISE EXCEPTION 'report content is immutable'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER lab_report_immutable BEFORE UPDATE OR DELETE ON lab_reports FOR EACH ROW EXECUTE FUNCTION lab_report_immutable();
CREATE FUNCTION lab_booking_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.submitted_at IS NOT NULL OR OLD.payment_status='paid' THEN
  IF NEW.lab_flow<>OLD.lab_flow OR NEW.lab_cost<>OLD.lab_cost OR (OLD.lab_flow AND NEW.sample<>OLD.sample) THEN RAISE EXCEPTION 'laboratory agreement is immutable'; END IF;
 END IF;
 IF NEW.lab_flow AND NEW.status='result_ready' AND NOT EXISTS(SELECT 1 FROM lab_reports WHERE id=NEW.lab_report_id AND booking_id=NEW.id AND state='published') THEN RAISE EXCEPTION 'published laboratory report required'; END IF;
 IF NEW.lab_flow AND (NEW.submitted_at IS NOT NULL OR NEW.payment_status='paid') AND (NEW.pricing_snapshot->'Lab' IS NULL OR NEW.lab_cost IS DISTINCT FROM (NEW.pricing_snapshot->'Lab'->>'Cost')::bigint OR NEW.amount<>((NEW.pricing_snapshot->>'Base')::bigint+(NEW.pricing_snapshot->>'Markup')::bigint+(NEW.pricing_snapshot->>'Fixed')::bigint+NEW.lab_cost)) THEN RAISE EXCEPTION 'laboratory pricing snapshot required'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lab_booking_guard BEFORE UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION lab_booking_guard();

CREATE OR REPLACE FUNCTION referral_refresh_order(p_booking text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE b bookings; s referral_settings; policy referral_booking_policy; uid text; rid text; base bigint; fee bigint;
BEGIN
 SELECT * INTO b FROM bookings WHERE id=p_booking;
 IF b.payment_status<>'paid' THEN RETURN; END IF;
 SELECT * INTO s FROM referral_settings WHERE id=1 FOR SHARE;
 INSERT INTO referral_booking_policy(booking_id,enabled,rate_bps,basis,revision)
 VALUES(b.id,s.enabled AND s.provider_enabled,CASE b.service WHEN 'vet' THEN s.vet_bps WHEN 'grooming' THEN s.grooming_bps WHEN 'sampling' THEN s.sampling_bps ELSE 0 END,s.basis,s.revision) ON CONFLICT DO NOTHING;
 SELECT * INTO policy FROM referral_booking_policy WHERE booking_id=b.id;
 SELECT p.user_id,r.referrer_id INTO uid,rid FROM providers p JOIN referrals r ON r.invitee_id=p.user_id AND r.enabled JOIN referral_codes c ON c.user_id=r.referrer_id AND c.enabled WHERE p.id=b.provider_id AND p.verified AND p.service=b.service FOR SHARE OF p,r,c;
 IF NOT FOUND OR NOT policy.enabled OR policy.rate_bps=0 THEN
  UPDATE referral_order_awards SET status='cancelled' WHERE booking_id=b.id AND status='pending'; RETURN;
 END IF;
 fee:=GREATEST(0,b.amount-b.lab_cost-COALESCE(b.provider_due,b.amount-b.amount*b.commission_bps/10000));
 IF policy.basis='platform_share' THEN base:=fee;
 ELSIF b.pricing_snapshot IS NOT NULL THEN base:=LEAST(fee,GREATEST(0,COALESCE((b.pricing_snapshot->>'Markup')::bigint,0)));
 ELSE base:=LEAST(fee,b.amount*b.commission_bps/10000); END IF;
 INSERT INTO referral_order_awards(booking_id,referrer_id,provider_user_id,basis_amount,amount,rate_bps,basis)
 VALUES(b.id,rid,uid,base,base*policy.rate_bps/10000,policy.rate_bps,policy.basis)
 ON CONFLICT(booking_id) DO UPDATE SET referrer_id=EXCLUDED.referrer_id,provider_user_id=EXCLUDED.provider_user_id,basis_amount=EXCLUDED.basis_amount,amount=EXCLUDED.amount,rate_bps=EXCLUDED.rate_bps,basis=EXCLUDED.basis,status='pending'
 WHERE referral_order_awards.status IN ('pending','cancelled') AND referral_order_awards.credited=0;
END $$;
