-- +goose Up
-- Text files that are not pages (csv, json, yaml, code) are a fifth entry kind.
ALTER TABLE entry DROP CONSTRAINT entry_kind_check;
ALTER TABLE entry ADD CONSTRAINT entry_kind_check CHECK (kind IN ('directory', 'page', 'config', 'asset', 'file'));

-- +goose Down
ALTER TABLE entry DROP CONSTRAINT entry_kind_check;
ALTER TABLE entry ADD CONSTRAINT entry_kind_check CHECK (kind IN ('directory', 'page', 'config', 'asset'));
