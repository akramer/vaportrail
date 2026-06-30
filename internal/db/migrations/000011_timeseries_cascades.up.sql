CREATE TRIGGER IF NOT EXISTS targets_delete_cleanup
BEFORE DELETE ON targets
BEGIN
    DELETE FROM results WHERE target_id = OLD.id;
    DELETE FROM raw_results WHERE target_id = OLD.id;
    DELETE FROM aggregated_results WHERE target_id = OLD.id;
    DELETE FROM dashboard_graph_targets WHERE target_id = OLD.id;
END;
