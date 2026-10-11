CREATE TABLE business_config (kind text NOT NULL,id text NOT NULL,data jsonb NOT NULL,revision bigint NOT NULL DEFAULT 1,PRIMARY KEY(kind,id));
CREATE TABLE business_audit (id bigserial PRIMARY KEY,actor_id text REFERENCES users(id),kind text NOT NULL,target text NOT NULL,before_data jsonb,after_data jsonb,created_at timestamptz NOT NULL DEFAULT now());
ALTER TABLE bookings ADD COLUMN commission_bps integer NOT NULL DEFAULT 0 CHECK(commission_bps BETWEEN 0 AND 10000);
CREATE TABLE business_payouts (booking_id text PRIMARY KEY REFERENCES bookings(id),provider_id text NOT NULL REFERENCES providers(id),amount bigint NOT NULL CHECK(amount>=0),reference text NOT NULL,actor_id text REFERENCES users(id),created_at timestamptz NOT NULL DEFAULT now());
