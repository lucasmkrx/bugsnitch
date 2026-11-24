SELECT json_object('projectId' VALUE project_id, 'attemptId' VALUE attempt_id, 'policyVersion' VALUE policy_version, 'reportVersion' VALUE report_version, 'approvalVersion' VALUE approval_version, 'issueNumber' VALUE issue_number,
       'number' VALUE number, 'candidateSha' VALUE candidate_sha, 'baseSha' VALUE base_sha,
       'target' VALUE target, 'status' VALUE status, 'reason' VALUE nullif(reason,''),
       'updatedAt' VALUE updated_at ABSENT ON NULL) AS document
FROM draft_pr_intents WHERE workspace_id=$1 ORDER BY updated_at DESC,attempt_id LIMIT 500
