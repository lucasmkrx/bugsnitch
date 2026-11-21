// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"bytes"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

type dashboardAsset struct {
	name     string
	modified time.Time
	data     []byte
}

type dashboardAssets map[string]dashboardAsset

// Snapshot the configured public bundle during handler construction. A request
// selects an existing response by URL. It never supplies a filesystem name.
func loadDashboard(dir string) dashboardAssets {
	if dir == "" {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		slog.Warn("dashboard directory unavailable", "error", err)
		return nil
	}
	defer root.Close()
	bundle := root.FS()
	index, err := fs.Stat(bundle, "index.html")
	if err != nil || !index.Mode().IsRegular() {
		slog.Warn("dashboard index unavailable")
		return nil
	}
	assets := dashboardAssets{}
	err = fs.WalkDir(bundle, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if name != "." && strings.HasPrefix(entry.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(bundle, name)
		if err != nil {
			return err
		}
		assets["/"+name] = dashboardAsset{name: name, modified: info.ModTime(), data: data}
		return nil
	})
	if err != nil {
		slog.Warn("dashboard bundle unavailable", "error", err)
		return nil
	}
	if _, ok := assets["/index.html"]; !ok {
		return nil
	}
	return assets
}

func (assets dashboardAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	asset, ok := assets[r.URL.Path]
	if ok && strings.HasSuffix(r.URL.Path, "/index.html") {
		destination := "./"
		if r.URL.RawQuery != "" {
			destination += "?" + r.URL.RawQuery
		}
		w.Header().Set("Location", destination)
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}
	if !ok && path.Ext(r.URL.Path) == "" {
		asset, ok = assets["/index.html"]
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, asset.name, asset.modified, bytes.NewReader(asset.data))
}
