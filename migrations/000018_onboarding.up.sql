ALTER TABLE otp_challenges ADD COLUMN next_path text NOT NULL DEFAULT '/dashboard';
ALTER TABLE otp_challenges ADD COLUMN login_intent text NOT NULL DEFAULT 'owner' CHECK(login_intent IN ('owner','provider','management'));
CREATE TABLE google_login_attempts (
 state text PRIMARY KEY, session_token text NOT NULL REFERENCES sessions(token) ON DELETE CASCADE,
 verifier text NOT NULL, nonce text NOT NULL, next_path text NOT NULL, intent text NOT NULL CHECK(intent IN ('owner','provider')),
 referral_code text NOT NULL DEFAULT '', subject text NOT NULL DEFAULT '', email text NOT NULL DEFAULT '', name text NOT NULL DEFAULT '',
 expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes', consumed boolean NOT NULL DEFAULT false
);
CREATE INDEX google_login_session ON google_login_attempts(session_token,expires_at);
ALTER TABLE otp_challenges ADD COLUMN google_attempt text REFERENCES google_login_attempts(state) ON DELETE SET NULL;
CREATE TABLE google_identities (
 subject text PRIMARY KEY, user_id text NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
 email text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE application_documents (
 id text PRIMARY KEY, application_id text NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 label text NOT NULL, filename text NOT NULL, mime text NOT NULL, data bytea NOT NULL CHECK(octet_length(data) BETWEEN 1 AND 8388608),
 created_at timestamptz NOT NULL DEFAULT now()
);
-- Remove the old generic license question only from non-veterinary forms.
UPDATE business_config SET data=jsonb_set(data,'{questions}',COALESCE((SELECT jsonb_agg(q ORDER BY n) FROM jsonb_array_elements(data->'questions') WITH ORDINALITY AS qs(q,n) WHERE q->>'id'<>'license'),'[]'::jsonb)), revision=revision+1
 WHERE kind='form' AND id IN ('signup-grooming','signup-sampling') AND data->'questions' IS NOT NULL;

ALTER TABLE applications ADD COLUMN documents_required boolean NOT NULL DEFAULT false;

-- Upgrade only the untouched default; keep administrator-edited requirements.
UPDATE business_config SET data=jsonb_set(data,'{questions}',(SELECT jsonb_agg(CASE WHEN q->>'id'='full_name' THEN jsonb_set(q,'{required}','true') ELSE q END ORDER BY n) FROM jsonb_array_elements(data->'questions') WITH ORDINALITY AS qs(q,n))),revision=revision+1
 WHERE kind='form' AND id='signup-owner' AND revision=1 AND EXISTS(SELECT 1 FROM jsonb_array_elements(data->'questions') q WHERE q->>'id'='full_name');
