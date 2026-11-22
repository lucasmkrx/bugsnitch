// SPDX-License-Identifier: MPL-2.0
// Package testgithub serves protocol fixtures on a loopback listener. It is
// imported only by tests so the packaged provider exercises real HTTP.
package testgithub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func Serve(t *testing.T, fn func(*http.Request) (*http.Response, error)) string {
	t.Helper()
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		response, err := fn(r)
		if err != nil {
			panic(http.ErrAbortHandler)
		} // the peer loses its response after a possible write
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, v := range values {
				w.Header().Add(key, v)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	return server.URL
}
