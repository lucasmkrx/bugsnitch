// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"errors"

	"github.com/lucasmkrx/bugsnitch/internal/storage"
)

func (s *Server) coordinate(ctx context.Context, operation string, input, output any) error {
	err := storage.Coordinate(ctx, s.DB, operation, input, output)
	if errors.Is(err, storage.ErrUnknownOutcome) {
		return problem{503, "Operation outcome unknown. Refresh its status before retrying."}
	}
	var native *storage.CoordinationError
	if errors.As(err, &native) {
		switch native.Status {
		case 400, 403, 404, 409, 503:
			return problem{native.Status, native.Message}
		}
	}
	return err
}
