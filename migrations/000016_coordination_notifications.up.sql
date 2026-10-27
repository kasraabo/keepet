ALTER TABLE consultation_chats ADD COLUMN kind text NOT NULL DEFAULT 'vet' CHECK(kind IN ('vet','grooming','sampling'));
ALTER TABLE consultation_chats ALTER COLUMN expires_at DROP NOT NULL,ALTER COLUMN duration_minutes DROP NOT NULL;
ALTER TABLE consultation_chats ADD CONSTRAINT chat_kind_window CHECK((kind='vet' AND expires_at IS NOT NULL AND duration_minutes IS NOT NULL) OR (kind<>'vet' AND expires_at IS NULL AND duration_minutes IS NULL));
CREATE FUNCTION chat_is_open(c consultation_chats,b bookings) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT c.closed_at IS NULL AND CASE WHEN c.kind='vet' THEN c.expires_at>now() AND b.status IN ('in_progress','completed')
 ELSE b.payment_status='paid' AND b.refund_status='' AND b.service=c.kind AND b.owner_id=c.owner_id AND EXISTS(SELECT 1 FROM providers p WHERE p.id=b.provider_id AND p.user_id=c.vet_id) AND b.status IN ('accepted','in_progress','awaiting_customer','collected','delivery_pending','delivered') END
$$;
CREATE FUNCTION coordination_booking_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.request_flow AND NEW.service IN ('grooming','sampling') AND NEW.payment_status='paid' AND NEW.refund_status='' AND NEW.status IN ('accepted','in_progress','awaiting_customer','collected','delivery_pending','delivered') THEN
  INSERT INTO consultation_chats(id,booking_id,owner_id,vet_id,kind,expires_at,duration_minutes)
  SELECT replace(gen_random_uuid()::text,'-','')||replace(gen_random_uuid()::text,'-',''),NEW.id,NEW.owner_id,p.user_id,NEW.service,NULL,NULL FROM providers p WHERE p.id=NEW.provider_id AND p.user_id IS NOT NULL AND NEW.owner_id IS NOT NULL ON CONFLICT(booking_id) DO NOTHING;
 END IF;
 UPDATE consultation_chats c SET closed_at=now(),close_reason='service_finished' WHERE c.booking_id=NEW.id AND c.kind<>'vet' AND c.closed_at IS NULL AND NOT chat_is_open(c,NEW);
 RETURN NEW;
END $$;
-- gen_random_uuid is built in; generate a full opaque identifier without extensions.
CREATE TRIGGER coordination_booking_event AFTER INSERT OR UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION coordination_booking_event();
CREATE TABLE chat_reads(chat_id text NOT NULL REFERENCES consultation_chats(id) ON DELETE CASCADE,user_id text NOT NULL REFERENCES users(id),last_message_id bigint NOT NULL DEFAULT 0 CHECK(last_message_id>=0),updated_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(chat_id,user_id));
-- Tracking starts at this release; historical messages must not all become new.
INSERT INTO chat_reads(chat_id,user_id,last_message_id)
SELECT c.id,u.user_id,max(m.id) FROM consultation_chats c JOIN consultation_messages m ON m.chat_id=c.id CROSS JOIN LATERAL unnest(ARRAY[c.owner_id,c.vet_id]) u(user_id) GROUP BY c.id,u.user_id ON CONFLICT DO NOTHING;
CREATE TABLE notifications(id bigserial PRIMARY KEY,user_id text NOT NULL REFERENCES users(id),booking_id text REFERENCES bookings(id),chat_id text REFERENCES consultation_chats(id),message_id bigint REFERENCES consultation_messages(id),kind text NOT NULL,event text NOT NULL DEFAULT '',event_key text UNIQUE NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),read_at timestamptz);
CREATE INDEX notifications_user ON notifications(user_id,id DESC);
CREATE INDEX notifications_unread ON notifications(user_id,id) WHERE read_at IS NULL;
CREATE FUNCTION notify_chat_message() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 INSERT INTO notifications(user_id,booking_id,chat_id,message_id,kind,event_key) SELECT CASE WHEN NEW.sender_id=c.owner_id THEN c.vet_id ELSE c.owner_id END,c.booking_id,c.id,NEW.id,'chat_message','message:'||NEW.id FROM consultation_chats c WHERE c.id=NEW.chat_id AND NEW.sender_id IN (c.owner_id,c.vet_id);
 RETURN NEW;
END $$;
CREATE TRIGGER notify_chat_message AFTER INSERT ON consultation_messages FOR EACH ROW EXECUTE FUNCTION notify_chat_message();
CREATE FUNCTION notify_booking_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE b bookings; uid text;
BEGIN
 IF NEW.event NOT IN ('pending_ops','pending_provider','awaiting_payment','request_rejected','request_expired','request_cancelled','paid','assigned','accepted','in_progress','arrival_requested','attendance_verified','service_reported_done','handover_uploaded','handover_approved','handover_rejected','service_issue','service_issue_resolved','collected','delivered','completed','result_ready','cancel_requested','cancelled','awaiting_report') THEN RETURN NEW; END IF;
 SELECT * INTO b FROM bookings WHERE id=NEW.booking_id;
 IF b.owner_id IS NOT NULL AND b.owner_id IS DISTINCT FROM NEW.actor_id THEN
  INSERT INTO notifications(user_id,booking_id,kind,event,event_key) VALUES(b.owner_id,b.id,'booking_event',NEW.event,'booking:'||NEW.id||':'||b.owner_id) ON CONFLICT DO NOTHING;
 END IF;
 -- An operator-gated request is not disclosed to the provider prematurely.
 IF b.status<>'pending_ops' AND (b.status='pending_provider' OR b.payment_status='paid') THEN
  SELECT user_id INTO uid FROM providers WHERE id=b.provider_id;
  IF uid IS NOT NULL AND uid IS DISTINCT FROM NEW.actor_id THEN
   INSERT INTO notifications(user_id,booking_id,kind,event,event_key) VALUES(uid,b.id,'booking_event',NEW.event,'booking:'||NEW.id||':'||uid) ON CONFLICT DO NOTHING;
  END IF;
 END IF;
 IF NEW.event IN ('pending_ops','service_issue','handover_uploaded','awaiting_report','cancel_requested') THEN
  INSERT INTO notifications(user_id,booking_id,kind,event,event_key) SELECT u.id,b.id,'operations_event',NEW.event,'booking:'||NEW.id||':'||u.id FROM users u WHERE u.role IN ('admin','ops') AND u.id IS DISTINCT FROM NEW.actor_id ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER notify_booking_event AFTER INSERT ON booking_events FOR EACH ROW EXECUTE FUNCTION notify_booking_event();

INSERT INTO consultation_chats(id,booking_id,owner_id,vet_id,kind,expires_at,duration_minutes)
SELECT replace(gen_random_uuid()::text,'-','')||replace(gen_random_uuid()::text,'-',''),b.id,b.owner_id,p.user_id,b.service,NULL,NULL FROM bookings b JOIN providers p ON p.id=b.provider_id WHERE b.request_flow AND b.payment_status='paid' AND b.refund_status='' AND b.service IN ('grooming','sampling') AND b.status IN ('accepted','in_progress','awaiting_customer','collected','delivery_pending','delivered') AND b.owner_id IS NOT NULL AND p.user_id IS NOT NULL ON CONFLICT(booking_id) DO NOTHING;
CREATE FUNCTION notify_response_reminder() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.kind='provider_response_reminder' AND NEW.user_id IS NOT NULL THEN
  INSERT INTO notifications(user_id,booking_id,kind,event,event_key) VALUES(NEW.user_id,NEW.booking_id,'booking_event','provider_response_reminder','reminder:'||NEW.id) ON CONFLICT DO NOTHING;
 END IF; RETURN NEW;
END $$;
CREATE TRIGGER notify_response_reminder AFTER INSERT ON outbox FOR EACH ROW EXECUTE FUNCTION notify_response_reminder();
