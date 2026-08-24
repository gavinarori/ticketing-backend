-- Append-only ledger of every status transition on event_seat_inventory.
-- Populated automatically by a trigger — not by service code — so
-- auditability doesn't depend on every future code path remembering to
-- log. Satisfies "full auditability of inventory changes" independent of
-- the application layer above it.
CREATE TABLE inventory_audit_log (
    -- Plain bigserial, not UUID: this table is append-only and
    -- high-volume (one row per status transition, ever), and has no
    -- anti-enumeration requirement — a sequence is cheaper to index and
    -- naturally orders by insertion time.
    id                       BIGSERIAL PRIMARY KEY,
    tenant_id                UUID NOT NULL,
    event_seat_inventory_id  UUID NOT NULL,
    previous_status          TEXT,
    new_status               TEXT NOT NULL,
    hold_token               UUID,
    held_by_user_id          UUID,
    changed_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (event_seat_inventory_id, tenant_id)
        REFERENCES event_seat_inventory(id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX idx_audit_inventory_id ON inventory_audit_log(event_seat_inventory_id);
CREATE INDEX idx_audit_tenant_id ON inventory_audit_log(tenant_id);
CREATE INDEX idx_audit_changed_at ON inventory_audit_log(changed_at);

CREATE OR REPLACE FUNCTION log_inventory_status_change()
RETURNS TRIGGER AS $$
BEGIN
    -- Only log on creation or an actual status change — an UPDATE that
    -- touches unrelated columns (there currently are none, but future
    -- ones might exist) shouldn't spam the ledger.
    IF TG_OP = 'INSERT' OR OLD.status IS DISTINCT FROM NEW.status THEN
        INSERT INTO inventory_audit_log (
            tenant_id, event_seat_inventory_id, previous_status, new_status,
            hold_token, held_by_user_id
        ) VALUES (
            NEW.tenant_id,
            NEW.id,
            CASE WHEN TG_OP = 'INSERT' THEN NULL ELSE OLD.status END,
            NEW.status,
            NEW.hold_token,
            NEW.held_by_user_id
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_esi_audit
    AFTER INSERT OR UPDATE ON event_seat_inventory
    FOR EACH ROW EXECUTE FUNCTION log_inventory_status_change();
