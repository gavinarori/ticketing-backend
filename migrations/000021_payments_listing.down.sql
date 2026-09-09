ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_listing_fk;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_order_or_listing;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_order_fk;
DROP INDEX IF EXISTS idx_payments_listing_id;
ALTER TABLE payments DROP COLUMN IF EXISTS listing_id;
ALTER TABLE payments ALTER COLUMN order_id SET NOT NULL;
ALTER TABLE payments ADD CONSTRAINT payments_order_id_tenant_id_fkey
    FOREIGN KEY (order_id, tenant_id) REFERENCES orders(id, tenant_id) ON DELETE RESTRICT;
