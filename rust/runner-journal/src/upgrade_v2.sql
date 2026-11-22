-- Preserve existing attempts and control receipts as generation 1.
CREATE TABLE IF NOT EXISTS local_projects(workspace TEXT NOT NULL,repo TEXT NOT NULL,data TEXT NOT NULL,PRIMARY KEY(workspace,repo));
CREATE TABLE IF NOT EXISTS local_attempts(id TEXT PRIMARY KEY,workspace TEXT NOT NULL,repo TEXT NOT NULL,issue INTEGER NOT NULL,version INTEGER NOT NULL,data TEXT NOT NULL,UNIQUE(workspace,repo,issue));
CREATE TABLE IF NOT EXISTS control_receipts(command_id TEXT PRIMARY KEY,attempt_id TEXT NOT NULL REFERENCES local_attempts(id),op TEXT NOT NULL,subject_version INTEGER NOT NULL,status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS journal_attempts(id TEXT PRIMARY KEY,workspace TEXT NOT NULL,repo TEXT NOT NULL,issue INTEGER NOT NULL,version INTEGER NOT NULL,data TEXT NOT NULL,generation INTEGER NOT NULL CHECK(generation BETWEEN 1 AND 3),UNIQUE(workspace,repo,issue,generation));
CREATE TABLE IF NOT EXISTS journal_controls(command_id TEXT PRIMARY KEY,attempt_id TEXT NOT NULL REFERENCES journal_attempts(id),op TEXT NOT NULL,subject_version INTEGER NOT NULL,status TEXT NOT NULL);
INSERT OR IGNORE INTO journal_attempts SELECT id,workspace,repo,issue,version,data,1 FROM local_attempts;
INSERT OR IGNORE INTO journal_controls SELECT * FROM control_receipts;
DROP TABLE control_receipts;
DROP TABLE local_attempts;
PRAGMA user_version=2;
