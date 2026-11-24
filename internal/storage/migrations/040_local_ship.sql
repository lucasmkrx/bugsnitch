ALTER TABLE draft_pr_intents
  ADD COLUMN local_ship boolean NOT NULL DEFAULT false,
  ADD COLUMN approval_version bigint NOT NULL DEFAULT 1 CHECK (approval_version > 0),
  DROP CONSTRAINT draft_pr_intents_status_check,
  ADD CONSTRAINT draft_pr_intents_status_check
    CHECK (status IN ('queued','reconciling','created','blocked') OR (local_ship AND status IN ('awaiting_publication','withdrawn')));
