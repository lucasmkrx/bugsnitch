// SPDX-License-Identifier: MPL-2.0
// Package audit records the authenticated source and version-bound facts for a
// durable event in the transaction that changes its subject.
package audit

import (
	"context"
	"database/sql"
)

type Source string

const (
	Owner    Source = "owner"
	Plugin   Source = "plugin"
	Device   Source = "device"
	Service  Source = "service"
	Provider Source = "provider"
)

type principal struct {
	source Source
	id     string
}
type actorKey struct{}
type detailsKey struct{}

type Details struct {
	Reason         string
	PolicyVersion  int64
	SubjectVersion int64
	EvidenceRef    string
}

// WithActor takes server-verified identities, never client attribution headers.
func WithActor(ctx context.Context, source Source, id string) context.Context {
	return context.WithValue(ctx, actorKey{}, principal{source, id})
}

// WithDetails accepts fixed explanations and verified references, never raw
// model output, human feedback, request bodies or credentials.
func WithDetails(ctx context.Context, d Details) context.Context {
	return context.WithValue(ctx, detailsKey{}, d)
}

func Record(ctx context.Context, tx *sql.Tx, workspace, project, kind, subject string) error {
	a, _ := ctx.Value(actorKey{}).(principal)
	d, _ := ctx.Value(detailsKey{}).(Details)
	_, err := tx.ExecContext(ctx, `INSERT INTO events(workspace_id,project_id,kind,subject_id,actor_source,actor_id,reason,policy_version,subject_version,evidence_ref)
	VALUES($1,NULLIF($2,''),$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8::bigint,0),NULLIF($9::bigint,0),NULLIF($10,''))`,
		workspace, project, kind, subject, a.source, a.id, d.Reason, d.PolicyVersion, d.SubjectVersion, d.EvidenceRef)
	return err
}
