INSERT INTO business_config(kind,id,data) VALUES
 ('site_details','contacts','{"phone":"","address":"","socials":[]}'),
 ('interface','persian','{"enabled":true,"fields":["full_name","name","pet_name","address","bio","body","description"]}') ON CONFLICT DO NOTHING;
CREATE INDEX IF NOT EXISTS bookings_operations_queue ON bookings(status,created_at DESC) WHERE status<>'draft';
CREATE INDEX IF NOT EXISTS applications_status_queue ON applications(status,created_at DESC);
CREATE INDEX IF NOT EXISTS tickets_status_queue ON tickets(status,created_at DESC);
