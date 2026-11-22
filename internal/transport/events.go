// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (s *Server) eventPage(w http.ResponseWriter, r *http.Request, ws string) error {
	var after int64
	limit := 50
	q := r.URL.Query()
	if q.Has("after") {
		n, err := strconv.ParseInt(q.Get("after"), 10, 64)
		if err != nil || n < 0 {
			return bad("Use a nonnegative event cursor")
		}
		after = n
	}
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil || n < 1 || n > 150 {
			return bad("Event page size must be between 1 and 150")
		}
		limit = n
	}
	var out json.RawMessage
	if err := s.coordinate(r.Context(), "events.replay", object{"workspace": ws, "after": after, "limit": limit}, &out); err != nil {
		return err
	}
	return send(w, 200, out)
}
