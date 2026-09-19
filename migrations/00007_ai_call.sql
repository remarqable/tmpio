-- +goose Up
-- Audit of language-model calls made on a tenant's behalf (placement suggestions).
-- No content is stored: only the purpose, model, token counts, latency and outcome.
CREATE TABLE ai_call (
  id             BIGSERIAL PRIMARY KEY,
  tenant_id      BIGINT NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
  principal_kind TEXT NOT NULL,
  purpose        TEXT NOT NULL,
  model          TEXT NOT NULL,
  input_tokens   INTEGER NOT NULL DEFAULT 0,
  output_tokens  INTEGER NOT NULL DEFAULT 0,
  latency_ms     INTEGER NOT NULL DEFAULT 0,
  outcome        TEXT NOT NULL,
  detail         TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_ai_call_tenant_created ON ai_call (tenant_id, created_at DESC);
ALTER TABLE ai_call ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_call FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ai_call
  USING (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT)
  WITH CHECK (tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::BIGINT);

-- +goose Down
DROP TABLE IF EXISTS ai_call;
