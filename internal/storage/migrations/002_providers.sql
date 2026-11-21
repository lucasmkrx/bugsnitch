-- Only the trusted operator can bind provider identities to a project.
CREATE TABLE sentry_mappings (
  workspace_id text NOT NULL, project_id text NOT NULL,
  installation_uuid uuid NOT NULL, provider_project bigint NOT NULL CHECK(provider_project>0),
  active boolean NOT NULL DEFAULT true,
  PRIMARY KEY (installation_uuid,provider_project),
  FOREIGN KEY (workspace_id,project_id) REFERENCES projects(workspace_id,id)
);
CREATE TABLE github_mappings (
  workspace_id text NOT NULL, project_id text NOT NULL,
  installation_id bigint NOT NULL CHECK(installation_id>0), repository_id bigint NOT NULL UNIQUE CHECK(repository_id>0),
  active boolean NOT NULL DEFAULT true,
  PRIMARY KEY(workspace_id,project_id),
  UNIQUE(workspace_id,project_id,installation_id,repository_id),
  FOREIGN KEY (workspace_id,project_id) REFERENCES projects(workspace_id,id)
);

CREATE TABLE incident_inbox (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, schema_version integer NOT NULL DEFAULT 1,
  workspace_id text NOT NULL, project_id text NOT NULL, source text NOT NULL,
  event_key text NOT NULL, group_key text NOT NULL, observed text NOT NULL,
  expected text NOT NULL DEFAULT '', steps text NOT NULL DEFAULT '',
  release text NOT NULL DEFAULT '', environment text NOT NULL DEFAULT '', frames jsonb NOT NULL DEFAULT '[]',
  seen_at timestamptz NOT NULL, accepted_at timestamptz NOT NULL DEFAULT now(), processed boolean NOT NULL DEFAULT false,
  UNIQUE(workspace_id,project_id,source,event_key),
  FOREIGN KEY (workspace_id,project_id) REFERENCES projects(workspace_id,id)
);
CREATE INDEX incident_inbox_pending ON incident_inbox(id) WHERE NOT processed;

CREATE TABLE incidents (
  id text PRIMARY KEY, workspace_id text NOT NULL, project_id text NOT NULL, source text NOT NULL,
  group_key text NOT NULL, observed text NOT NULL, expected text NOT NULL, steps text NOT NULL,
  first_seen timestamptz NOT NULL, last_seen timestamptz NOT NULL, occurrences bigint NOT NULL,
  version bigint NOT NULL DEFAULT 1, state text NOT NULL DEFAULT 'Needs reproduction',
  UNIQUE(workspace_id,project_id,source,group_key), UNIQUE(workspace_id,project_id,id),
  FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id)
);

CREATE TABLE incident_issues (
  workspace_id text NOT NULL, project_id text NOT NULL, incident_id text NOT NULL PRIMARY KEY,
  installation_id bigint NOT NULL, repository_id bigint NOT NULL, marker text NOT NULL UNIQUE,
  approved_version bigint NOT NULL, approved_at timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL CHECK(status IN ('queued','reconciling','linked','blocked')),
  attempt_count integer NOT NULL DEFAULT 0, next_run timestamptz NOT NULL DEFAULT now(),
  reason text NOT NULL DEFAULT '', number bigint, issue_id bigint, github_state text, requirements_digest text,
  checked_at timestamptz,
  UNIQUE(repository_id,number),
  FOREIGN KEY(workspace_id,project_id,incident_id) REFERENCES incidents(workspace_id,project_id,id),
  FOREIGN KEY(workspace_id,project_id,installation_id,repository_id)
    REFERENCES github_mappings(workspace_id,project_id,installation_id,repository_id)
);
CREATE INDEX incident_issue_work ON incident_issues(next_run) WHERE status <> 'blocked';
CREATE TABLE github_deliveries (
  workspace_id text NOT NULL, project_id text NOT NULL, delivery text NOT NULL,
  accepted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(workspace_id,project_id,delivery),
  FOREIGN KEY(workspace_id,project_id) REFERENCES projects(workspace_id,id)
);
CREATE INDEX incident_observations ON incident_inbox(workspace_id,project_id,source,group_key,seen_at DESC);
CREATE INDEX incident_rate_window ON incident_inbox(workspace_id,project_id,accepted_at) WHERE source='sentry';

-- Append-only owner attestations. A rollback is a new deployment on the same environment.
CREATE TABLE incident_evidence (
  workspace_id text NOT NULL, project_id text NOT NULL, incident_id text NOT NULL,
  command_key text NOT NULL, payload_hash text NOT NULL, kind text NOT NULL CHECK(kind IN ('fix','deployment','validation')),
  candidate text NOT NULL, environment text NOT NULL, build text NOT NULL, evidence text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT now(), effective_at timestamptz NOT NULL,
  source text NOT NULL DEFAULT 'authenticated owner', positive boolean, coverage boolean, exposures bigint,
  monitor_seconds integer NOT NULL DEFAULT 0, minimum_exposures integer NOT NULL DEFAULT 0,
  PRIMARY KEY(workspace_id,project_id,incident_id,command_key),
  FOREIGN KEY(workspace_id,project_id,incident_id) REFERENCES incidents(workspace_id,project_id,id)
);
CREATE INDEX incident_current_evidence ON incident_evidence(incident_id,kind,environment,effective_at DESC);
