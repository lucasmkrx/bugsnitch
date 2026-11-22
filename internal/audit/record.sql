INSERT INTO events(workspace_id,project_id,kind,subject_id,actor_source,actor_id,reason,policy_version,subject_version,evidence_ref)
VALUES($1,NULLIF($2,''),$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8::bigint,0),NULLIF($9::bigint,0),NULLIF($10,''))
