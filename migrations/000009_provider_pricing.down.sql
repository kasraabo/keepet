DROP TRIGGER booking_pricing_immutable ON bookings;
DROP FUNCTION booking_pricing_immutable();
-- Preserve old settlement semantics before removing snapshots.
CREATE OR REPLACE FUNCTION wallet_booking_credit() RETURNS trigger LANGUAGE plpgsql AS $$
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
ALTER TABLE bookings DROP COLUMN provider_due,DROP COLUMN pricing_snapshot;
DROP TABLE provider_tariffs;
ALTER TABLE providers DROP COLUMN hourly_rate,DROP COLUMN tariff_revision;
