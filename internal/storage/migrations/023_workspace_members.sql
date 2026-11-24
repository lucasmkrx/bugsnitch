CREATE TABLE workspace_members(
 workspace_id text NOT NULL REFERENCES workspaces,
 id text NOT NULL,
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
 role text NOT NULL CHECK (role IN ('operator','approver')),
 token_hash text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 revoked_at timestamptz,
 PRIMARY KEY(workspace_id,id)
);
ALTER TABLE sessions
 ADD COLUMN member_id text,
 ADD FOREIGN KEY(workspace_id,member_id) REFERENCES workspace_members(workspace_id,id);
CREATE INDEX sessions_member ON sessions(workspace_id,member_id) WHERE member_id IS NOT NULL;

ALTER TABLE events DROP CONSTRAINT events_actor_source_check;
ALTER TABLE events ADD CHECK (actor_source IN ('owner','operator','approver','plugin','device','service','provider'));
