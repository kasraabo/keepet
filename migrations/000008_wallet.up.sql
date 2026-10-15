-- Integer toman only. Cash is withdrawable; promotional credit cannot be cashed out.
CREATE TABLE wallets (
 user_id text PRIMARY KEY REFERENCES users(id),
 cash bigint NOT NULL DEFAULT 0 CHECK(cash>=0),
 bonus bigint NOT NULL DEFAULT 0 CHECK(bonus>=0),
 reserved bigint NOT NULL DEFAULT 0 CHECK(reserved>=0),
 frozen boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallet_entries (
 id bigserial PRIMARY KEY, user_id text NOT NULL REFERENCES wallets(user_id),
 kind text NOT NULL, cash_delta bigint NOT NULL DEFAULT 0, bonus_delta bigint NOT NULL DEFAULT 0,
 reserved_delta bigint NOT NULL DEFAULT 0,
 cash_after bigint NOT NULL, bonus_after bigint NOT NULL, reserved_after bigint NOT NULL,
 idempotency_key text UNIQUE NOT NULL, booking_id text REFERENCES bookings(id),
 actor_id text REFERENCES users(id), description text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(cash_delta<>0 OR bonus_delta<>0 OR reserved_delta<>0)
);
CREATE INDEX wallet_entries_user_time ON wallet_entries(user_id,id DESC);
CREATE FUNCTION wallet_post(p_user text,p_kind text,p_cash bigint,p_bonus bigint,p_reserved bigint,p_key text,p_booking text,p_actor text,p_description text) RETURNS boolean LANGUAGE plpgsql AS $$
DECLARE w wallets; old wallet_entries;
BEGIN
 INSERT INTO wallets(user_id) VALUES(p_user) ON CONFLICT DO NOTHING;
 SELECT * INTO w FROM wallets WHERE user_id=p_user FOR UPDATE;
 SELECT * INTO old FROM wallet_entries WHERE idempotency_key=p_key;
 IF FOUND THEN
  IF old.user_id<>p_user OR old.kind<>p_kind OR old.cash_delta<>p_cash OR old.bonus_delta<>p_bonus OR old.reserved_delta<>p_reserved OR old.booking_id IS DISTINCT FROM p_booking THEN RAISE EXCEPTION 'wallet idempotency conflict'; END IF;
  RETURN false;
 END IF;
 IF w.cash+p_cash<0 OR w.bonus+p_bonus<0 OR w.reserved+p_reserved<0 THEN RAISE EXCEPTION 'insufficient wallet balance'; END IF;
 UPDATE wallets SET cash=cash+p_cash,bonus=bonus+p_bonus,reserved=reserved+p_reserved,updated_at=now() WHERE user_id=p_user RETURNING * INTO w;
 INSERT INTO wallet_entries(user_id,kind,cash_delta,bonus_delta,reserved_delta,cash_after,bonus_after,reserved_after,idempotency_key,booking_id,actor_id,description)
 VALUES(p_user,p_kind,p_cash,p_bonus,p_reserved,w.cash,w.bonus,w.reserved,p_key,p_booking,p_actor,p_description);
 RETURN true;
END $$;
CREATE FUNCTION wallet_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'financial record is immutable; post a compensating entry'; END $$;
CREATE TRIGGER wallet_entries_immutable BEFORE UPDATE OR DELETE ON wallet_entries FOR EACH ROW EXECUTE FUNCTION wallet_immutable();
CREATE TABLE wallet_bank_accounts (
 id text PRIMARY KEY, user_id text UNIQUE NOT NULL REFERENCES users(id), iban text NOT NULL CHECK(iban ~ '^IR[0-9]{24}$'),
 holder text NOT NULL, status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','verified','rejected')),
 revision bigint NOT NULL DEFAULT 1, reviewer_id text REFERENCES users(id), review_note text NOT NULL DEFAULT '',
 reviewed_at timestamptz, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE wallet_withdrawals (
 id text PRIMARY KEY,user_id text NOT NULL REFERENCES wallets(user_id),amount bigint NOT NULL CHECK(amount>0),
 bank_id text NOT NULL REFERENCES wallet_bank_accounts(id),bank_revision bigint NOT NULL,iban text NOT NULL,holder text NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','processing','paid','rejected','cancelled')),
 request_key text UNIQUE NOT NULL,operator_id text REFERENCES users(id),reason text NOT NULL DEFAULT '',
 sender_name text NOT NULL DEFAULT '',sender_account text NOT NULL DEFAULT '',receipt bytea,receipt_hash text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),processing_at timestamptz,paid_at timestamptz,updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(status<>'paid' OR (receipt IS NOT NULL AND length(sender_name)>0 AND length(sender_account)>0 AND paid_at IS NOT NULL AND operator_id IS NOT NULL))
);
CREATE INDEX wallet_withdrawals_queue ON wallet_withdrawals(status,created_at);
CREATE UNIQUE INDEX wallet_receipt_unique ON wallet_withdrawals(receipt_hash) WHERE receipt_hash<>'';
CREATE FUNCTION wallet_paid_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.status='paid' THEN RAISE EXCEPTION 'paid withdrawal is immutable'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER wallet_withdrawals_paid_immutable BEFORE UPDATE OR DELETE ON wallet_withdrawals FOR EACH ROW EXECUTE FUNCTION wallet_paid_immutable();
CREATE TABLE wallet_settings (
 id integer PRIMARY KEY CHECK(id=1),min_withdrawal bigint NOT NULL DEFAULT 100000 CHECK(min_withdrawal BETWEEN 1 AND 1000000000),
 max_withdrawal bigint NOT NULL DEFAULT 50000000 CHECK(max_withdrawal>=min_withdrawal AND max_withdrawal<=1000000000),
 daily_limit bigint NOT NULL DEFAULT 100000000 CHECK(daily_limit>=max_withdrawal AND daily_limit<=10000000000),
 withdrawals_enabled boolean NOT NULL DEFAULT true
);
INSERT INTO wallet_settings(id) VALUES(1);
ALTER TABLE bookings ADD COLUMN wallet_cash_used bigint NOT NULL DEFAULT 0 CHECK(wallet_cash_used>=0),ADD COLUMN wallet_bonus_used bigint NOT NULL DEFAULT 0 CHECK(wallet_bonus_used>=0);
CREATE FUNCTION wallet_booking_credit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE uid text; due bigint; bonus_refund bigint;
BEGIN
 IF NEW.payment_status='paid' AND NEW.status IN ('completed','result_ready') AND NEW.refund_status='' AND NEW.provider_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM business_payouts WHERE booking_id=NEW.id) THEN
  SELECT user_id INTO uid FROM providers WHERE id=NEW.provider_id;
  due:=NEW.amount-NEW.amount*NEW.commission_bps/10000;
  IF uid IS NOT NULL AND due>0 THEN PERFORM wallet_post(uid,'service_income',due,0,0,'income:'||NEW.id,NEW.id,NULL,'سهم خدمت تکمیل‌شده'); END IF;
 END IF;
 IF NEW.refund_status='refunded' AND OLD.refund_status IS DISTINCT FROM 'refunded' AND NEW.owner_id IS NOT NULL AND NEW.refund_amount>0 THEN
  bonus_refund:=LEAST(NEW.refund_amount,NEW.wallet_bonus_used);
  PERFORM wallet_post(NEW.owner_id,'refund',NEW.refund_amount-bonus_refund,bonus_refund,0,'refund:'||NEW.id,NEW.id,NULL,'بازگشت وجه رزرو به کیف پول');
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_booking_credit AFTER UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION wallet_booking_credit();
-- Historical completed services already settled through the old route must never be credited again.
SELECT wallet_post(p.user_id,'service_income',b.amount-b.amount*b.commission_bps/10000,0,0,'income:'||b.id,b.id,NULL,'انتقال سهم خدمت تکمیل‌شدهٔ قبلی به کیف پول')
FROM bookings b JOIN providers p ON p.id=b.provider_id WHERE b.payment_status='paid' AND b.status IN ('completed','result_ready') AND b.refund_status='' AND b.amount-b.amount*b.commission_bps/10000>0 AND NOT EXISTS(SELECT 1 FROM business_payouts x WHERE x.booking_id=b.id);
