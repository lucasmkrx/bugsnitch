// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"net/http"
)

func (s *Server) workspaceControl(w http.ResponseWriter, r *http.Request, ws string) error {
	var b struct {
		Stopped   *bool
		RequestID string
		Version   int64
	}
	if err := decode(w, r, &b); err != nil {
		return err
	}
	if b.Stopped == nil || b.Version < 1 || !keyPattern.MatchString(b.RequestID) {
		return bad("Provide the operation, current version and request ID")
	}
	var result struct {
		Stopped bool  `json:"stopped"`
		Version int64 `json:"version"`
	}
	err := s.coordinate(r.Context(), "device.workspace_control", object{
		"workspace": ws, "stopped": *b.Stopped, "version": b.Version, "requestId": b.RequestID,
	}, &result)
	if err != nil {
		return err
	}
	return send(w, 200, result)
}
