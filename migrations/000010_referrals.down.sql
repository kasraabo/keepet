-- Financial referral history and debt are lost by this rollback. Export and back up first.
DROP TRIGGER z_referral_booking_event ON bookings;
DROP FUNCTION referral_booking_event();
DROP TRIGGER aa_referral_booking_locks ON bookings;
DROP FUNCTION referral_booking_locks();
DROP FUNCTION referral_refresh_order(text);
DROP TRIGGER referral_identity_immutable ON referrals;
DROP FUNCTION referral_identity_immutable();
DROP FUNCTION referral_register(text,text);
DROP TRIGGER referral_cash_credit ON wallet_entries;
DROP FUNCTION referral_cash_credit();
DROP FUNCTION referral_sweep(text);
DROP TRIGGER referral_award_immutable ON referral_order_awards;
DROP FUNCTION referral_award_immutable();
DROP INDEX referral_completed_provider;
DROP TABLE referral_order_awards,referral_booking_policy,referral_events,referral_accounts,referrals,referral_codes,referral_settings;
ALTER TABLE otp_challenges DROP COLUMN referral_code;
