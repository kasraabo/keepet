CREATE TABLE care_followups(
 id text PRIMARY KEY,source_booking_id text NOT NULL REFERENCES bookings(id),source_chat_id text NOT NULL REFERENCES consultation_chats(id),owner_id text NOT NULL REFERENCES users(id),doctor_id text NOT NULL REFERENCES users(id),provider_id text NOT NULL REFERENCES providers(id),pet_id text REFERENCES pets(id),service text NOT NULL CHECK(service IN ('vet','grooming','sampling')),day date NOT NULL,period text NOT NULL CHECK(period IN ('morning','noon','afternoon','night')),preferred_at timestamptz,note text NOT NULL DEFAULT '' CHECK(length(note)<=2000),state text NOT NULL DEFAULT 'offered' CHECK(state IN ('offered','started','dismissed','withdrawn')),booking_id text REFERENCES bookings(id),form_key text UNIQUE NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX care_followups_open ON care_followups(source_booking_id,service) WHERE state IN ('offered','started');
CREATE INDEX care_followups_owner ON care_followups(owner_id,created_at DESC);
CREATE INDEX care_followups_doctor ON care_followups(doctor_id,created_at DESC);
ALTER TABLE bookings ADD COLUMN followup_id text REFERENCES care_followups(id);
CREATE FUNCTION care_followup_guard() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'care history is immutable'; END IF;
 IF TG_OP='INSERT' THEN
  IF NOT EXISTS(SELECT 1 FROM bookings b JOIN consultation_chats c ON c.id=NEW.source_chat_id AND c.booking_id=b.id JOIN providers p ON p.id=b.provider_id JOIN users u ON u.id=p.user_id WHERE b.id=NEW.source_booking_id AND b.service='vet' AND b.payment_status='paid' AND b.status='completed' AND c.kind='vet' AND c.closed_at IS NOT NULL AND EXISTS(SELECT 1 FROM consultation_vet_forms f WHERE f.chat_id=c.id) AND b.owner_id=NEW.owner_id AND p.id=NEW.provider_id AND p.user_id=NEW.doctor_id AND c.vet_id=NEW.doctor_id AND p.active AND p.verified AND u.role='provider') THEN RAISE EXCEPTION 'completed treating vet visit required'; END IF;
  IF NEW.pet_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pets WHERE id=NEW.pet_id AND user_id=NEW.owner_id) THEN RAISE EXCEPTION 'patient pet required'; END IF;
 ELSE
  IF (to_jsonb(NEW)-ARRAY['state','booking_id','updated_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['state','booking_id','updated_at']) THEN RAISE EXCEPTION 'care recommendation is immutable'; END IF;
  IF OLD.state IN ('dismissed','withdrawn') AND NEW IS DISTINCT FROM OLD THEN RAISE EXCEPTION 'closed recommendation is immutable'; END IF;
  IF OLD.state='started' AND NEW.state<>'started' THEN RAISE EXCEPTION 'started recommendation cannot be withdrawn'; END IF;
 END IF;
 IF (NEW.state='started') IS DISTINCT FROM (NEW.booking_id IS NOT NULL) THEN RAISE EXCEPTION 'care reservation state must match'; END IF;
 IF NEW.booking_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM bookings WHERE id=NEW.booking_id AND owner_id=NEW.owner_id AND service=NEW.service AND followup_id=NEW.id) THEN RAISE EXCEPTION 'matching followup reservation required'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER care_followup_guard BEFORE INSERT OR UPDATE OR DELETE ON care_followups FOR EACH ROW EXECUTE FUNCTION care_followup_guard();
CREATE FUNCTION care_booking_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE f care_followups; src bookings;
BEGIN
 IF TG_OP='UPDATE' AND OLD.followup_id IS DISTINCT FROM NEW.followup_id THEN RAISE EXCEPTION 'care source cannot be replaced'; END IF;
 IF NEW.followup_id IS NULL THEN RETURN NEW; END IF;
 SELECT * INTO f FROM care_followups WHERE id=NEW.followup_id;SELECT * INTO src FROM bookings WHERE id=f.source_booking_id;
 IF NEW.owner_id IS DISTINCT FROM f.owner_id OR NEW.service IS DISTINCT FROM f.service OR (f.pet_id IS NOT NULL AND COALESCE(NEW.answers->>'_pet_id','')<>f.pet_id) OR (f.pet_id IS NULL AND (NEW.pet_name<>src.pet_name OR NEW.species<>src.species)) THEN RAISE EXCEPTION 'care patient and service must match'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER care_booking_guard BEFORE INSERT OR UPDATE ON bookings FOR EACH ROW EXECUTE FUNCTION care_booking_guard();

ALTER TABLE notifications ADD COLUMN followup_id text REFERENCES care_followups(id);
