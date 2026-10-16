ALTER TABLE providers ADD COLUMN hourly_rate bigint CHECK(hourly_rate BETWEEN 1 AND 1000000000), ADD COLUMN tariff_revision bigint NOT NULL DEFAULT 1;
CREATE TABLE provider_tariffs (
 provider_id text NOT NULL REFERENCES providers(id), service_id text NOT NULL,
 price bigint NOT NULL CHECK(price BETWEEN 1 AND 1000000000), enabled boolean NOT NULL DEFAULT true,
 PRIMARY KEY(provider_id,service_id)
);
ALTER TABLE bookings ADD COLUMN provider_due bigint CHECK(provider_due>=0 AND provider_due<=amount), ADD COLUMN pricing_snapshot jsonb;
CREATE OR REPLACE FUNCTION wallet_booking_credit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE uid text; due bigint; bonus_refund bigint;
BEGIN
 IF NEW.payment_status='paid' AND NEW.status IN ('completed','result_ready') AND NEW.refund_status='' AND NEW.provider_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM business_payouts WHERE booking_id=NEW.id) THEN
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
-- A paid quote is the financial agreement; later changes use separate ledger entries.
CREATE FUNCTION booking_pricing_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.payment_status='paid' AND (NEW.amount IS DISTINCT FROM OLD.amount OR NEW.provider_due IS DISTINCT FROM OLD.provider_due OR NEW.pricing_snapshot IS DISTINCT FROM OLD.pricing_snapshot OR NEW.commission_bps IS DISTINCT FROM OLD.commission_bps) THEN
  RAISE EXCEPTION 'paid booking pricing is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER booking_pricing_immutable BEFORE UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION booking_pricing_immutable();
