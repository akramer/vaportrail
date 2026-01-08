-- Add commit_interval column back (cannot restore data)
ALTER TABLE targets ADD COLUMN commit_interval REAL DEFAULT 60.0;
