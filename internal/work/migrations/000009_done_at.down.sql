DROP INDEX IF EXISTS work_items_done_at_idx;
DROP TRIGGER IF EXISTS work_items_done_at ON work_items;
DROP FUNCTION IF EXISTS work.work_items_done_at();
ALTER TABLE work_items DROP COLUMN IF EXISTS done_at;
