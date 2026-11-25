ALTER TABLE workspace_members
    ADD COLUMN login_email text CHECK (login_email IS NULL OR role = 'operator');

CREATE UNIQUE INDEX workspace_members_active_login_email ON workspace_members(lower(login_email))
    WHERE login_email IS NOT NULL AND revoked_at IS NULL;

CREATE TABLE email_sign_in_codes (
    workspace_id text NOT NULL,
    member_id text NOT NULL,
    challenge_hash text NOT NULL UNIQUE,
    code_mac text NOT NULL,
    sent_at timestamptz NOT NULL,
    send_count smallint NOT NULL CHECK (send_count BETWEEN 1 AND 5),
    attempts smallint NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    consumed boolean NOT NULL DEFAULT false,
    PRIMARY KEY (workspace_id, member_id),
    FOREIGN KEY (workspace_id, member_id) REFERENCES workspace_members(workspace_id, id)
);
