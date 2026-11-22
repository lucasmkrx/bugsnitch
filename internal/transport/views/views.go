// SPDX-License-Identifier: MPL-2.0
// Package views contains the database projections shared by the hosted read
// service and Go domain operations returning the same records after a write.
package views

import _ "embed"

//go:embed pulls.sql
var Pulls string

//go:embed incidents.sql
var Incidents string

//go:embed drafts.sql
var Drafts string
