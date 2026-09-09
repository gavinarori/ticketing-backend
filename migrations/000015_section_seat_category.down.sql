ALTER TABLE venue_sections DROP CONSTRAINT IF EXISTS venue_sections_seat_category_fk;
DROP INDEX IF EXISTS idx_venue_sections_seat_category;
ALTER TABLE venue_sections DROP COLUMN IF EXISTS seat_category_id;
