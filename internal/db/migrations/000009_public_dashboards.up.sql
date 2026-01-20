-- SQLite doesn't support adding UNIQUE columns with ALTER TABLE directly
-- So we need to use a safe approach: add columns without UNIQUE constraint first,
-- then create a unique index separately

-- Add is_public column
ALTER TABLE dashboards ADD COLUMN is_public BOOLEAN DEFAULT FALSE;

-- Add public_slug column (without UNIQUE constraint in ALTER)
ALTER TABLE dashboards ADD COLUMN public_slug TEXT;

-- Create unique index on public_slug
CREATE UNIQUE INDEX IF NOT EXISTS idx_dashboards_public_slug ON dashboards(public_slug);
