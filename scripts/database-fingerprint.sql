-- Temporary objects only. Row contents and private blobs never leave PostgreSQL.
CREATE TEMP TABLE keepet_backup_fingerprint(table_name text, row_count bigint, row_digest text);
DO $$
DECLARE r record;
BEGIN
 FOR r IN SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename LOOP
  EXECUTE format('INSERT INTO keepet_backup_fingerprint SELECT %L,count(*),md5(COALESCE(string_agg(h,'''' ORDER BY h),'''')) FROM (SELECT md5(row_to_json(t)::text) h FROM public.%I t) x',r.tablename,r.tablename);
 END LOOP;
END $$;
SELECT jsonb_object_agg(table_name,jsonb_build_object('count',row_count,'digest',row_digest)) FROM keepet_backup_fingerprint;
