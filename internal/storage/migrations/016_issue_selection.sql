-- Preserve the exact GitHub revision only when its bounded, sanitized title
-- still matches the canonical title used by local-start. Older rows wait for a read.
ALTER TABLE planned_issue_state ADD COLUMN selection_revision text NOT NULL DEFAULT '';
