DROP TRIGGER IF EXISTS trg_esi_audit ON event_seat_inventory;
DROP FUNCTION IF EXISTS log_inventory_status_change();
DROP TABLE IF EXISTS inventory_audit_log;
