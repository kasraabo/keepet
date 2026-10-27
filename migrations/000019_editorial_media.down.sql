DROP TABLE magazine_layout;
DROP TABLE editorial_asset_links;
DROP TABLE editorial_assets;
DELETE FROM business_config WHERE kind='seo' AND id IN ('directory-vet','directory-grooming') AND revision=1;
