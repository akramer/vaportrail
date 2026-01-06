-- Revert: Clear retention policies (set back to empty)
-- Note: This is a destructive operation - only use if you want to clear ALL retention policies
UPDATE targets SET retention_policies = NULL WHERE retention_policies IS NOT NULL;
