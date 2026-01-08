-- Set default retention policies for existing targets that don't have them
UPDATE targets 
SET retention_policies = '[{"window":0,"retention":604800},{"window":60,"retention":15768000},{"window":300,"retention":31536000},{"window":3600,"retention":315360000},{"window":86400,"retention":3153600000}]'
WHERE retention_policies IS NULL OR retention_policies = '' OR retention_policies = '[]';
