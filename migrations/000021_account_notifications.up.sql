-- Source references contain identifiers only; never copy bank details, ticket bodies or drafts.
ALTER TABLE notifications ADD COLUMN source_id text NOT NULL DEFAULT '', ADD COLUMN source_user_id text REFERENCES users(id);
ALTER TABLE wallet_withdrawals ADD COLUMN notification_revision bigint NOT NULL DEFAULT 1;
ALTER TABLE tickets ADD COLUMN notification_revision bigint NOT NULL DEFAULT 1;

CREATE FUNCTION notify_wallet_entry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.kind NOT IN ('withdrawal_hold','withdrawal_release','withdrawal_paid') THEN
  INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
  VALUES(NEW.user_id,'wallet_entry',NEW.kind,'wallet-entry:'||NEW.id,NEW.id::text,NEW.user_id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_entry_notification AFTER INSERT ON wallet_entries FOR EACH ROW EXECUTE FUNCTION notify_wallet_entry();

CREATE FUNCTION notify_bank_account() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.status=OLD.status AND NEW.revision=OLD.revision THEN RETURN NEW; END IF;
 END IF;
 INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
 VALUES(NEW.user_id,'bank_account',NEW.status,'bank:'||NEW.id||':'||NEW.revision||':'||NEW.status,NEW.id,NEW.user_id) ON CONFLICT DO NOTHING;
 IF NEW.status='pending' THEN
  INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
  SELECT id,'wallet_operations','bank_review','bank-review:'||NEW.id||':'||NEW.revision||':'||id,NEW.id,NEW.user_id FROM users WHERE role IN ('ops','admin') AND id<>NEW.user_id ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bank_account_notification AFTER INSERT OR UPDATE ON wallet_bank_accounts FOR EACH ROW EXECUTE FUNCTION notify_bank_account();

CREATE FUNCTION notify_withdrawal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.status=OLD.status THEN RETURN NEW; END IF;
  NEW.notification_revision=OLD.notification_revision+1;
 END IF;
 INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
 VALUES(NEW.user_id,'withdrawal',NEW.status,'withdrawal:'||NEW.id||':'||NEW.notification_revision||':'||NEW.user_id,NEW.id,NEW.user_id) ON CONFLICT DO NOTHING;
 IF NEW.status='pending' THEN
  INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
  SELECT id,'wallet_operations','withdrawal_pending','withdrawal:'||NEW.id||':'||NEW.notification_revision||':'||id,NEW.id,NEW.user_id FROM users WHERE role IN ('ops','admin') AND id<>NEW.user_id ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER withdrawal_notification BEFORE INSERT OR UPDATE ON wallet_withdrawals FOR EACH ROW EXECUTE FUNCTION notify_withdrawal();

CREATE FUNCTION notify_ticket() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
  SELECT id,'ticket_operations','created','ticket-created:'||NEW.id||':'||id,NEW.id,NEW.user_id FROM users WHERE role IN ('ops','admin') AND id<>NEW.user_id ON CONFLICT DO NOTHING;
 ELSIF NEW.reply IS DISTINCT FROM OLD.reply OR NEW.status IS DISTINCT FROM OLD.status THEN
  NEW.notification_revision=OLD.notification_revision+1;
  INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
  VALUES(NEW.user_id,'ticket','updated','ticket:'||NEW.id||':'||NEW.notification_revision,NEW.id,NEW.user_id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ticket_notification BEFORE INSERT OR UPDATE ON tickets FOR EACH ROW EXECUTE FUNCTION notify_ticket();

CREATE FUNCTION notify_editorial_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE ev text;
BEGIN
 IF NEW.kind NOT IN ('article','page','seo','magazine_layout','scientific_review') OR NEW.actor_id IS NULL THEN RETURN NEW; END IF;
 IF (COALESCE(NEW.before_data,'{}'::jsonb)-'Revision'-'revision')=(COALESCE(NEW.after_data,'{}'::jsonb)-'Revision'-'revision') THEN RETURN NEW; END IF;
 ev:='updated';
 IF NEW.kind='magazine_layout' THEN ev:='layout';
 ELSIF NEW.kind='scientific_review' THEN ev:='review';
 ELSIF COALESCE((NEW.after_data->>'enabled')::boolean,false) AND NOT COALESCE((NEW.before_data->>'enabled')::boolean,false) THEN ev:='published';
 ELSIF NOT COALESCE((NEW.after_data->>'enabled')::boolean,false) AND COALESCE((NEW.before_data->>'enabled')::boolean,false) THEN ev:='unpublished'; END IF;
 INSERT INTO notifications(user_id,kind,event,event_key,source_id)
 SELECT id,'editorial',ev,'editorial:'||NEW.id||':'||id,NEW.kind||':'||NEW.target FROM users WHERE role IN ('admin','content') AND id<>NEW.actor_id ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER editorial_change_notification AFTER INSERT ON business_audit FOR EACH ROW EXECUTE FUNCTION notify_editorial_change();

ALTER TABLE wallets ADD COLUMN notification_revision bigint NOT NULL DEFAULT 1;
CREATE FUNCTION notify_wallet_freeze() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.frozen IS DISTINCT FROM OLD.frozen THEN
  NEW.notification_revision=OLD.notification_revision+1;
  INSERT INTO notifications(user_id,kind,event,event_key,source_id,source_user_id)
  VALUES(NEW.user_id,'wallet_account',CASE WHEN NEW.frozen THEN 'frozen' ELSE 'active' END,'wallet-state:'||NEW.user_id||':'||NEW.notification_revision,NEW.user_id,NEW.user_id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER wallet_freeze_notification BEFORE UPDATE ON wallets FOR EACH ROW EXECUTE FUNCTION notify_wallet_freeze();
