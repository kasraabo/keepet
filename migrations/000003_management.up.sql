CREATE TABLE platform_integrations (
 id text PRIMARY KEY,
 settings jsonb NOT NULL DEFAULT '{}',
 secret bytea,
 revision bigint NOT NULL DEFAULT 1,
 updated_by text REFERENCES users(id),
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO platform_integrations(id,settings) VALUES
 ('zarinpal','{"mode":"sandbox","callback_url":"","description":"پرداخت خدمات کیپت"}'),
 ('snappay','{"mode":"sandbox","merchant_id":"","api_base":"","callback_url":""}'),
 ('sms','{"provider":"kavenegar","sender":"","template":"","mode":"development"}'),
 ('email','{"host":"","port":"587","username":"","from":"","mode":"development"}'),
 ('otp','{"digits":"6","ttl":"120","resend":"30","hourly":"5","attempts":"5"}');
ALTER TABLE otp_challenges ADD COLUMN max_attempts integer NOT NULL DEFAULT 5;
