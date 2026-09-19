-- +goose Up
-- The four SECURITY DEFINER credential lookups (lookup_share_grant, lookup_api_token,
-- lookup_oauth_token, lookup_oauth_code) resolve a token hash to a tenant before any
-- tenant scope exists. They execute as the function owner, app_owner. On a managed
-- PostgreSQL that role is not a superuser and has no BYPASSRLS, so with FORCE ROW
-- LEVEL SECURITY the lookups saw no rows and every credential failed. The five
-- credential tables therefore let the owner role read (never write) rows without a
-- tenant scope. The runtime role app_user is unaffected: it is still confined to
-- its tenant on every table, and the lookups return identifiers only.
-- +goose StatementBegin
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['share_grant', 'api_token', 'oauth_grant', 'oauth_token', 'oauth_code'] LOOP
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I
         USING (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::BIGINT OR current_user = ''app_owner'')
         WITH CHECK (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::BIGINT)', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['share_grant', 'api_token', 'oauth_grant', 'oauth_token', 'oauth_code'] LOOP
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I
         USING (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::BIGINT)
         WITH CHECK (tenant_id = NULLIF(current_setting(''app.tenant_id'', true), '''')::BIGINT)', t);
  END LOOP;
END $$;
-- +goose StatementEnd
