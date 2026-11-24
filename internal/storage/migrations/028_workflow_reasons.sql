ALTER TABLE attempt_reports DROP CONSTRAINT attempt_reports_reason_code_check;
ALTER TABLE attempt_reports ADD CHECK (
 reason_code IN ('','sign_in_required','allowance_exhausted','model_unavailable','provider_rate_limited','provider_unavailable',
    'device_unauthorized','authority_unreachable','authority_stale','control_pending','local_capacity','task_failed')
 OR (state='waiting' AND reason_code IN ('plan_review_required','owner_input_required','review_correction','recovered_candidate','owner_paused'))
);
