ALTER TABLE pets ADD COLUMN birth_year text NOT NULL DEFAULT '';
ALTER TABLE tickets ADD COLUMN email text NOT NULL DEFAULT '';
CREATE TABLE form_responses (
 scope text NOT NULL, subject text NOT NULL, user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 questions jsonb NOT NULL, answers jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(scope,subject)
);
CREATE TABLE provider_profiles (
 provider_id text PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
 specialties text NOT NULL DEFAULT '', education text NOT NULL DEFAULT '', background text NOT NULL DEFAULT ''
);
CREATE TABLE provider_media (
 id text PRIMARY KEY, provider_id text NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK(kind IN ('portrait','gallery')), caption text NOT NULL DEFAULT '',
 mime text NOT NULL, data bytea NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX provider_portrait ON provider_media(provider_id) WHERE kind='portrait';
ALTER TABLE reviews ADD COLUMN published boolean NOT NULL DEFAULT false;
ALTER TABLE consultation_customer_forms ADD COLUMN published boolean NOT NULL DEFAULT false;
