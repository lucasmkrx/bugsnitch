CREATE TABLE member_chat_identities(
 workspace_id text NOT NULL,
 member_id text NOT NULL,
 provider text NOT NULL CHECK(provider IN ('slack','telegram')),
 tenant text NOT NULL,
 user_id text NOT NULL,
 PRIMARY KEY(provider,tenant,user_id),
 UNIQUE(workspace_id,member_id,provider),
 FOREIGN KEY(workspace_id,member_id) REFERENCES workspace_members(workspace_id,id)
);
