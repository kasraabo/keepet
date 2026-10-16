CREATE TABLE referral_settings (
 id integer PRIMARY KEY CHECK(id=1),revision bigint NOT NULL DEFAULT 1,
 enabled boolean NOT NULL DEFAULT false,referrer_bonus_enabled boolean NOT NULL DEFAULT false,invitee_bonus_enabled boolean NOT NULL DEFAULT false,provider_enabled boolean NOT NULL DEFAULT false,
 referrer_bonus bigint NOT NULL DEFAULT 0 CHECK(referrer_bonus BETWEEN 0 AND 1000000000),invitee_bonus bigint NOT NULL DEFAULT 0 CHECK(invitee_bonus BETWEEN 0 AND 1000000000),
 vet_bps integer NOT NULL DEFAULT 0 CHECK(vet_bps BETWEEN 0 AND 10000),grooming_bps integer NOT NULL DEFAULT 0 CHECK(grooming_bps BETWEEN 0 AND 10000),sampling_bps integer NOT NULL DEFAULT 0 CHECK(sampling_bps BETWEEN 0 AND 10000),
 basis text NOT NULL DEFAULT 'commission' CHECK(basis IN ('commission','platform_share'))
);
INSERT INTO referral_settings(id) VALUES(1);
CREATE TABLE referral_codes (
 user_id text PRIMARY KEY REFERENCES users(id),code text UNIQUE NOT NULL CHECK(code ~ '^KP-[A-F0-9]{12}$'),enabled boolean NOT NULL DEFAULT true,revision bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE referrals (
 invitee_id text PRIMARY KEY REFERENCES users(id),referrer_id text NOT NULL REFERENCES users(id),code text NOT NULL REFERENCES referral_codes(code),enabled boolean NOT NULL DEFAULT true,revision bigint NOT NULL DEFAULT 1,
 referrer_bonus bigint NOT NULL DEFAULT 0,invitee_bonus bigint NOT NULL DEFAULT 0,settings_revision bigint NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),CHECK(invitee_id<>referrer_id)
);
CREATE INDEX referrals_referrer ON referrals(referrer_id,created_at DESC,invitee_id);
ALTER TABLE otp_challenges ADD COLUMN referral_code text NOT NULL DEFAULT '';
CREATE TABLE referral_accounts(user_id text PRIMARY KEY REFERENCES users(id),debt bigint NOT NULL DEFAULT 0 CHECK(debt>=0));
CREATE TABLE referral_events (
 id bigserial PRIMARY KEY,user_id text NOT NULL REFERENCES users(id),invitee_id text REFERENCES users(id),booking_id text REFERENCES bookings(id),
 kind text NOT NULL,amount bigint NOT NULL CHECK(amount>0),cash_delta bigint NOT NULL DEFAULT 0,bonus_delta bigint NOT NULL DEFAULT 0,debt_delta bigint NOT NULL DEFAULT 0,
 event_key text UNIQUE NOT NULL,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX referral_events_user ON referral_events(user_id,id DESC);
CREATE TRIGGER referral_events_immutable BEFORE UPDATE OR DELETE ON referral_events FOR EACH ROW EXECUTE FUNCTION wallet_immutable();
CREATE TABLE referral_booking_policy (
 booking_id text PRIMARY KEY REFERENCES bookings(id),enabled boolean NOT NULL,rate_bps integer NOT NULL CHECK(rate_bps BETWEEN 0 AND 10000),basis text NOT NULL CHECK(basis IN ('commission','platform_share')),revision bigint NOT NULL,created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER referral_policy_immutable BEFORE UPDATE OR DELETE ON referral_booking_policy FOR EACH ROW EXECUTE FUNCTION wallet_immutable();
CREATE TABLE referral_order_awards (
 booking_id text PRIMARY KEY REFERENCES bookings(id),referrer_id text NOT NULL REFERENCES users(id),provider_user_id text NOT NULL REFERENCES users(id),
 basis_amount bigint NOT NULL CHECK(basis_amount>=0),amount bigint NOT NULL CHECK(amount>=0),rate_bps integer NOT NULL CHECK(rate_bps BETWEEN 0 AND 10000),basis text NOT NULL,
 credited bigint NOT NULL DEFAULT 0 CHECK(credited>=0 AND credited<=amount),adjusted bigint NOT NULL DEFAULT 0 CHECK(adjusted>=0 AND adjusted<=credited),
 status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','credited','cancelled')),created_at timestamptz NOT NULL DEFAULT now(),credited_at timestamptz
);
CREATE INDEX referral_awards_referrer ON referral_order_awards(referrer_id,status,created_at DESC);
-- Recover reversed rewards from available cash, never from promotional or reserved balances.
CREATE FUNCTION referral_sweep(p_user text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE w wallets; d bigint; take bigint; key text;
BEGIN
 SELECT * INTO w FROM wallets WHERE user_id=p_user FOR UPDATE; IF NOT FOUND THEN RETURN; END IF;
 INSERT INTO referral_accounts(user_id) VALUES(p_user) ON CONFLICT DO NOTHING;
 SELECT debt INTO d FROM referral_accounts WHERE user_id=p_user FOR UPDATE;
 take:=LEAST(d,w.cash); IF take<=0 THEN RETURN; END IF;
 UPDATE referral_accounts SET debt=debt-take WHERE user_id=p_user;
 INSERT INTO referral_events(user_id,kind,amount,cash_delta,debt_delta,event_key)
 VALUES(p_user,'debt_recovery',take,-take,-take,'recovery:'||gen_random_uuid()::text) RETURNING event_key INTO key;
 PERFORM wallet_post(p_user,'referral_recovery',-take,0,0,'referral:'||key,NULL,NULL,'وصول بدهی اصلاح پاداش معرفی');
END $$;
CREATE FUNCTION referral_cash_credit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.cash_delta>0 THEN PERFORM referral_sweep(NEW.user_id); END IF; RETURN NEW;
END $$;
CREATE TRIGGER referral_cash_credit AFTER INSERT ON wallet_entries FOR EACH ROW EXECUTE FUNCTION referral_cash_credit();
CREATE FUNCTION referral_register(p_invitee text,p_code text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE c referral_codes; s referral_settings; rb bigint:=0; ib bigint:=0; uid text;
BEGIN
 IF p_code='' THEN RETURN; END IF;
 SELECT * INTO c FROM referral_codes WHERE code=p_code AND enabled FOR SHARE; IF NOT FOUND OR c.user_id=p_invitee THEN RAISE EXCEPTION 'invalid referral code'; END IF;
 SELECT * INTO s FROM referral_settings WHERE id=1 FOR SHARE;
 IF s.enabled AND s.referrer_bonus_enabled THEN rb:=s.referrer_bonus; END IF;
 IF s.enabled AND s.invitee_bonus_enabled THEN ib:=s.invitee_bonus; END IF;
 INSERT INTO referrals(invitee_id,referrer_id,code,referrer_bonus,invitee_bonus,settings_revision) VALUES(p_invitee,c.user_id,c.code,rb,ib,s.revision) ON CONFLICT DO NOTHING;
 IF NOT FOUND THEN RETURN; END IF;
 -- Stable ordering also covers simultaneous registrations that use the same referrer.
 FOR uid IN SELECT unnest(ARRAY[p_invitee,c.user_id]) ORDER BY 1 LOOP
  INSERT INTO wallets(user_id) VALUES(uid) ON CONFLICT DO NOTHING;
  PERFORM user_id FROM wallets WHERE user_id=uid FOR UPDATE;
 END LOOP;
 IF rb>0 THEN
  INSERT INTO referral_events(user_id,invitee_id,kind,amount,bonus_delta,event_key) VALUES(c.user_id,p_invitee,'signup_referrer',rb,rb,'signup-referrer:'||p_invitee);
  PERFORM wallet_post(c.user_id,'referral_signup',0,rb,0,'referral:signup-referrer:'||p_invitee,NULL,NULL,'اعتبار هدیهٔ عضویت فرد معرفی‌شده');
 END IF;
 IF ib>0 THEN
  INSERT INTO referral_events(user_id,invitee_id,kind,amount,bonus_delta,event_key) VALUES(p_invitee,p_invitee,'signup_welcome',ib,ib,'signup-welcome:'||p_invitee);
  PERFORM wallet_post(p_invitee,'referral_welcome',0,ib,0,'referral:signup-welcome:'||p_invitee,NULL,NULL,'اعتبار خوش‌آمدگویی با کد معرف');
 END IF;
END $$;
-- Preserve attribution and the amounts agreed at verified signup. Management may pause future accrual only.
CREATE FUNCTION referral_identity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.invitee_id<>OLD.invitee_id OR NEW.referrer_id<>OLD.referrer_id OR NEW.code<>OLD.code OR NEW.referrer_bonus<>OLD.referrer_bonus OR NEW.invitee_bonus<>OLD.invitee_bonus OR NEW.settings_revision<>OLD.settings_revision OR NEW.created_at<>OLD.created_at THEN RAISE EXCEPTION 'referral identity is immutable'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER referral_identity_immutable BEFORE UPDATE ON referrals FOR EACH ROW EXECUTE FUNCTION referral_identity_immutable();
CREATE FUNCTION referral_refresh_order(p_booking text) RETURNS void LANGUAGE plpgsql AS $$
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
 fee:=GREATEST(0,b.amount-COALESCE(b.provider_due,b.amount-b.amount*b.commission_bps/10000));
 IF policy.basis='platform_share' THEN base:=fee;
 ELSIF b.pricing_snapshot IS NOT NULL THEN base:=LEAST(fee,GREATEST(0,COALESCE((b.pricing_snapshot->>'Markup')::bigint,0)));
 ELSE base:=LEAST(fee,b.amount*b.commission_bps/10000); END IF;
 INSERT INTO referral_order_awards(booking_id,referrer_id,provider_user_id,basis_amount,amount,rate_bps,basis)
 VALUES(b.id,rid,uid,base,base*policy.rate_bps/10000,policy.rate_bps,policy.basis)
 ON CONFLICT(booking_id) DO UPDATE SET referrer_id=EXCLUDED.referrer_id,provider_user_id=EXCLUDED.provider_user_id,basis_amount=EXCLUDED.basis_amount,amount=EXCLUDED.amount,rate_bps=EXCLUDED.rate_bps,basis=EXCLUDED.basis,status='pending'
 WHERE referral_order_awards.status IN ('pending','cancelled') AND referral_order_awards.credited=0;
END $$;
-- Lock every wallet touched by completion/refund before either financial trigger runs.
CREATE FUNCTION referral_booking_locks() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ids text[]; uid text;
BEGIN
 IF NEW.payment_status='paid' AND (NEW.status IN ('completed','result_ready') OR NEW.refund_status='refunded') THEN
  SELECT array_agg(DISTINCT x ORDER BY x) INTO ids FROM (
   SELECT NEW.owner_id x UNION SELECT user_id FROM providers WHERE id=NEW.provider_id UNION SELECT referrer_id FROM referral_order_awards WHERE booking_id=NEW.id UNION SELECT r.referrer_id FROM referrals r JOIN providers p ON p.user_id=r.invitee_id WHERE p.id=NEW.provider_id
  ) p WHERE x IS NOT NULL;
  FOREACH uid IN ARRAY COALESCE(ids,ARRAY[]::text[]) LOOP
   INSERT INTO wallets(user_id) VALUES(uid) ON CONFLICT DO NOTHING;
   PERFORM user_id FROM wallets WHERE user_id=uid FOR UPDATE;
  END LOOP;
 END IF; RETURN NEW;
END $$;
CREATE TRIGGER aa_referral_booking_locks BEFORE UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION referral_booking_locks();
CREATE FUNCTION referral_booking_event() RETURNS trigger LANGUAGE plpgsql AS $$
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
 ELSIF ((NEW.service='sampling' AND NEW.status='result_ready') OR (NEW.service<>'sampling' AND NEW.status='completed')) AND a.status='pending' AND a.amount=0 THEN
  UPDATE referral_order_awards SET status='cancelled' WHERE booking_id=NEW.id;
 ELSIF ((NEW.service='sampling' AND NEW.status='result_ready') OR (NEW.service<>'sampling' AND NEW.status='completed')) AND NEW.refund_status='' AND a.status='pending' AND a.amount>0 AND NOT EXISTS(SELECT 1 FROM business_payouts WHERE booking_id=NEW.id) THEN
  INSERT INTO referral_events(user_id,invitee_id,booking_id,kind,amount,cash_delta,event_key) VALUES(a.referrer_id,a.provider_user_id,NEW.id,'order_income',a.amount,a.amount,'income:'||NEW.id);
  PERFORM wallet_post(a.referrer_id,'referral_income',a.amount,0,0,'referral:income:'||NEW.id,NEW.id,NULL,'سهم معرف از کمیسیون خدمت تکمیل‌شده');
  UPDATE referral_order_awards SET credited=amount,status='credited',credited_at=now() WHERE booking_id=NEW.id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER z_referral_booking_event AFTER UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION referral_booking_event();
CREATE FUNCTION referral_award_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.credited>0 AND (NEW.booking_id<>OLD.booking_id OR NEW.referrer_id<>OLD.referrer_id OR NEW.provider_user_id<>OLD.provider_user_id OR NEW.basis_amount<>OLD.basis_amount OR NEW.amount<>OLD.amount OR NEW.rate_bps<>OLD.rate_bps OR NEW.basis<>OLD.basis OR NEW.credited<>OLD.credited OR NEW.status<>OLD.status OR NEW.credited_at IS DISTINCT FROM OLD.credited_at OR NEW.adjusted<OLD.adjusted) THEN RAISE EXCEPTION 'credited referral award is immutable'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER referral_award_immutable BEFORE UPDATE ON referral_order_awards FOR EACH ROW EXECUTE FUNCTION referral_award_immutable();
CREATE INDEX referral_awards_provider ON referral_order_awards(provider_user_id,referrer_id);
CREATE INDEX referral_events_invitee ON referral_events(invitee_id);
CREATE INDEX referral_completed_provider ON bookings(provider_id) WHERE payment_status='paid' AND status IN ('completed','result_ready');
