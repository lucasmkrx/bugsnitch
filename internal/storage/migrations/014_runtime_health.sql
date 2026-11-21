ALTER TABLE attempt_reports ADD COLUMN reason_code text NOT NULL DEFAULT ''
 CHECK(reason_code IN ('','sign_in_required','allowance_exhausted','model_unavailable','provider_rate_limited','provider_unavailable','device_unauthorized','authority_unreachable','authority_stale','control_pending','local_capacity','task_failed'));
