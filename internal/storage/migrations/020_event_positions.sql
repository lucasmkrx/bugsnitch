-- A sequence value is allocated before commit. Serialize publication within a
-- workspace so a consumer can advance past a page without missing a late commit.
CREATE TABLE workspace_event_positions (
    workspace_id text PRIMARY KEY REFERENCES workspaces(id),
    last_position bigint NOT NULL CHECK (last_position > 0)
);
ALTER TABLE events ADD COLUMN position bigint;
WITH ordered AS (
    SELECT seq,row_number() OVER (PARTITION BY workspace_id ORDER BY seq) AS position FROM events
)
UPDATE events e SET position=o.position FROM ordered o WHERE e.seq=o.seq;
INSERT INTO workspace_event_positions(workspace_id,last_position)
    SELECT workspace_id,max(position) FROM events GROUP BY workspace_id;
CREATE UNIQUE INDEX events_workspace_position ON events(workspace_id,position);

CREATE FUNCTION allocate_workspace_event_position() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    published bigint;
BEGIN
    INSERT INTO workspace_event_positions(workspace_id,last_position)
        VALUES(NEW.workspace_id,1)
        ON CONFLICT (workspace_id) DO UPDATE SET last_position=workspace_event_positions.last_position+1
        RETURNING last_position INTO published;
    UPDATE events SET position=published WHERE seq=NEW.seq;
    RETURN NULL;
END $$;
-- Every event inserted by an ordinary writer receives its position at commit,
-- after all domain locks (including locks taken by older server releases).
-- The event is invisible to other readers until this trigger finishes.
CREATE CONSTRAINT TRIGGER events_allocate_position AFTER INSERT ON events
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION allocate_workspace_event_position();
