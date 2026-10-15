-- Reversing this migration destroys the wallet ledger. Back up first.
DROP TRIGGER wallet_booking_credit ON bookings;
DROP FUNCTION wallet_booking_credit();
ALTER TABLE bookings DROP COLUMN wallet_cash_used,DROP COLUMN wallet_bonus_used;
DROP TABLE wallet_settings,wallet_withdrawals,wallet_bank_accounts;
DROP FUNCTION wallet_paid_immutable();
DROP TABLE wallet_entries;
DROP FUNCTION wallet_immutable();
DROP FUNCTION wallet_post(text,text,bigint,bigint,bigint,text,text,text);
DROP TABLE wallets;
