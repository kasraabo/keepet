ALTER TABLE consultation_customer_forms DROP COLUMN published;
ALTER TABLE reviews DROP COLUMN published;
DROP TABLE provider_media,provider_profiles,form_responses;
ALTER TABLE tickets DROP COLUMN email;
ALTER TABLE pets DROP COLUMN birth_year;
