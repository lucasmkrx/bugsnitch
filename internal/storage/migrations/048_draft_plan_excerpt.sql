-- Freeze exactly what the owner agreed to share. Earlier approvals keep their original PR body.
ALTER TABLE draft_pr_intents ADD COLUMN plan_excerpt text NOT NULL DEFAULT '';
