-- Reverting this migration requires a planned data migration for new requests.
DO $$ BEGIN IF EXISTS(SELECT 1 FROM bookings WHERE request_flow AND submitted_at IS NOT NULL) THEN RAISE EXCEPTION 'cannot remove live request workflow'; END IF; END $$;
DROP TRIGGER booking_request_guard ON bookings;
DROP FUNCTION booking_request_guard();
DROP FUNCTION booking_holds_capacity(text,text,timestamptz);
DROP INDEX booking_approval_pending;
ALTER TABLE bookings DROP COLUMN request_flow,DROP COLUMN requires_ops_approval,DROP COLUMN approval_policy,DROP COLUMN submitted_at,DROP COLUMN stage_deadline,DROP COLUMN provider_requested_at,DROP COLUMN request_reason;
DROP TABLE booking_approval_settings;
ALTER TABLE providers DROP COLUMN requires_ops_approval,DROP COLUMN approval_revision;
