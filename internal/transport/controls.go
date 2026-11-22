// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
)

type controlInput struct {
	Operation string `json:"operation"`
	RequestID string `json:"requestId"`
	Version   int64  `json:"version"`
}

func (s *Server) requestControl(ctx context.Context, ws, attempt string, b controlInput) (object, error) {
	if !keyPattern.MatchString(b.RequestID) || (b.Operation != "pause" && b.Operation != "stop") || b.Version < 1 {
		return nil, bad("Choose a control and the current attempt version")
	}
	var out object
	err := s.coordinate(ctx, "device.control", object{
		"workspace": ws, "attempt": attempt, "request": b, "newId": random(16),
	}, &out)
	return out, err
}
