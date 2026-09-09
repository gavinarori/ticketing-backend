ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_order_id_tenant_id_fkey;
ALTER TABLE payments ALTER COLUMN order_id DROP NOT NULL;
ALTER TABLE payments ADD COLUMN listing_id UUID;
ALTER TABLE payments ADD CONSTRAINT payments_order_or_listing CHECK (
    (order_id IS NOT NULL AND listing_id IS NULL)
    OR (order_id IS NULL AND listing_id IS NOT NULL)
);
ALTER TABLE payments ADD CONSTRAINT payments_order_fk
    FOREIGN KEY (order_id, tenant_id) REFERENCES orders(id, tenant_id) ON DELETE RESTRICT;
ALTER TABLE payments ADD CONSTRAINT payments_listing_fk
    FOREIGN KEY (listing_id, tenant_id) REFERENCES ticket_listings(id, tenant_id) ON DELETE RESTRICT;
CREATE INDEX idx_payments_listing_id ON payments(listing_id) WHERE listing_id IS NOT NULL;
