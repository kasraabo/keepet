CREATE TABLE consultation_chats (
 id text PRIMARY KEY,
 booking_id text NOT NULL UNIQUE REFERENCES bookings(id) ON DELETE CASCADE,
 owner_id text NOT NULL REFERENCES users(id),
 vet_id text NOT NULL REFERENCES users(id),
 started_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 duration_minutes integer NOT NULL CHECK(duration_minutes BETWEEN 5 AND 240),
 closed_at timestamptz,
 close_reason text NOT NULL DEFAULT ''
);
CREATE TABLE consultation_messages (
 id bigserial PRIMARY KEY,
 chat_id text NOT NULL REFERENCES consultation_chats(id) ON DELETE CASCADE,
 sender_id text NOT NULL REFERENCES users(id),
 body text NOT NULL DEFAULT '',
 kind text NOT NULL CHECK(kind IN ('text','image','audio')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX consultation_messages_chat ON consultation_messages(chat_id,id);
CREATE TABLE consultation_attachments (
 id text PRIMARY KEY,
 message_id bigint NOT NULL UNIQUE REFERENCES consultation_messages(id) ON DELETE CASCADE,
 mime text NOT NULL,
 filename text NOT NULL,
 data bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(octet_length(data)<=12582912)
);
CREATE TABLE consultation_vet_forms (
 chat_id text PRIMARY KEY REFERENCES consultation_chats(id) ON DELETE CASCADE,
 symptoms text NOT NULL,
 diagnosis text NOT NULL,
 prescription text NOT NULL,
 customer_rating integer CHECK(customer_rating BETWEEN 1 AND 5),
 customer_feedback text NOT NULL DEFAULT '',
 submitted_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE consultation_customer_forms (
 chat_id text PRIMARY KEY REFERENCES consultation_chats(id) ON DELETE CASCADE,
 rating integer NOT NULL CHECK(rating BETWEEN 1 AND 5),
 communication integer NOT NULL CHECK(communication BETWEEN 1 AND 5),
 clarity integer NOT NULL CHECK(clarity BETWEEN 1 AND 5),
 body text NOT NULL DEFAULT '',
 submitted_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX consultation_chats_owner ON consultation_chats(owner_id,started_at DESC);
CREATE INDEX consultation_chats_vet ON consultation_chats(vet_id,started_at DESC);
INSERT INTO business_config(kind,id,data) VALUES('chat','consultation','{"name":"گفتگوی دامپزشکی","duration_minutes":30,"enabled":true}') ON CONFLICT DO NOTHING;
