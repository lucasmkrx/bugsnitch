// SPDX-License-Identifier: MPL-2.0
// Package audit records the authenticated source and version-bound facts for a
// durable event in the transaction that changes its subject.
package audit

import (
	"context"
	"database/sql"
	_ "embed"
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

//go:embed record.sql
var insertEvent string

// Actor returns attribution established by the authenticated transport.
func Actor(ctx context.Context) (string, string) {
	a, _ := ctx.Value(actorKey{}).(principal)
	return string(a.source), a.id
}

func Record(ctx context.Context, tx *sql.Tx, workspace, project, kind, subject string) error {
	a, _ := ctx.Value(actorKey{}).(principal)
	d, _ := ctx.Value(detailsKey{}).(Details)
	_, err := tx.ExecContext(ctx, insertEvent,
		workspace, project, kind, subject, a.source, a.id, d.Reason, d.PolicyVersion, d.SubjectVersion, d.EvidenceRef)
	return err
}
