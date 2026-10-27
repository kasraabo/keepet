DROP TABLE application_documents;
DROP TABLE google_identities;
ALTER TABLE otp_challenges DROP COLUMN google_attempt;
DROP TABLE google_login_attempts;
ALTER TABLE otp_challenges DROP COLUMN next_path, DROP COLUMN login_intent;
ALTER TABLE applications DROP COLUMN documents_required;
