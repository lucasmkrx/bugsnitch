ALTER TABLE pull_evidence DROP CONSTRAINT pull_evidence_kind_check;
ALTER TABLE pull_evidence ADD CHECK(kind IN ('combined_validation','independent_review','sign_off'));
