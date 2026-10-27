CREATE TABLE editorial_assets (
 id text PRIMARY KEY, created_by text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 mime text NOT NULL CHECK(mime IN ('image/jpeg','video/mp4')),
 data bytea NOT NULL CHECK(octet_length(data) BETWEEN 1 AND 67108864),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX editorial_assets_creator ON editorial_assets(created_by,created_at);
CREATE TABLE editorial_asset_links (
 kind text NOT NULL, target text NOT NULL, asset_id text NOT NULL REFERENCES editorial_assets(id) ON DELETE CASCADE,
 PRIMARY KEY(kind,target,asset_id), FOREIGN KEY(kind,target) REFERENCES business_config(kind,id) ON DELETE CASCADE
);
CREATE INDEX editorial_asset_public ON editorial_asset_links(asset_id);
CREATE TABLE magazine_layout (
 id integer PRIMARY KEY CHECK(id=1), revision bigint NOT NULL DEFAULT 1,
 first_row text[] NOT NULL DEFAULT '{}',second_row text[] NOT NULL DEFAULT '{}',articles text[] NOT NULL DEFAULT '{}'
);
INSERT INTO magazine_layout(id) VALUES(1);
INSERT INTO business_config(kind,id,data) VALUES
 ('seo','directory-vet','{"name":"انتخاب دامپزشک آنلاین","description":"متخصص، زمان و هزینه مناسب را مقایسه کنید.","link":"/specialists?service=vet","body":"","enabled":false}'),
 ('seo','directory-grooming','{"name":"انتخاب گرومر","description":"خدمات و زمان گرومرهای کیپت را مقایسه کنید.","link":"/specialists?service=grooming","body":"","enabled":false}') ON CONFLICT DO NOTHING;
