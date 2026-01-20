-- Drop the unique index
DROP INDEX IF EXISTS idx_dashboards_public_slug;

-- SQLite doesn't support DROP COLUMN, so we just clear the values
UPDATE dashboards SET is_public = FALSE, public_slug = NULL;
