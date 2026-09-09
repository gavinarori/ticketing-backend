-- Maps a physical venue section to a reusable seat-category label so
-- reserved-seat publish can generate one inventory row per seat in every
-- section tagged with the ticket category's seat_category_id.
-- NULL is valid: GA sections (and legacy rows) have no mapping.

ALTER TABLE venue_sections
    ADD COLUMN seat_category_id UUID;

ALTER TABLE venue_sections
    ADD CONSTRAINT venue_sections_seat_category_fk
    FOREIGN KEY (seat_category_id, tenant_id)
    REFERENCES seat_categories(id, tenant_id)
    ON DELETE SET NULL;

CREATE INDEX idx_venue_sections_seat_category
    ON venue_sections(tenant_id, seat_category_id)
    WHERE seat_category_id IS NOT NULL;
