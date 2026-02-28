package static

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Manifest maps logical asset paths (e.g. "css/app.css") to content-hashed
// filenames (e.g. "css/app.a1b2c3d4e5f6.css") for cache busting.
type Manifest struct {
	dir     string
	hashes  map[string]string // logical → hashed ("css/app.css" → "css/app.a1b2c3d4e5f6.css")
	reverse map[string]string // hashed → logical (for serving)
}

// NewManifest walks dir, computes SHA-256 content hashes, and builds the mapping.
func NewManifest(dir string) (*Manifest, error) {
	m := &Manifest{
		dir:     dir,
		hashes:  make(map[string]string),
		reverse: make(map[string]string),
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// Normalise to forward slashes for URL paths.
		rel = filepath.ToSlash(rel)

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		hash := sha256.Sum256(data)
		hexHash := fmt.Sprintf("%x", hash[:6]) // first 12 hex chars

		ext := filepath.Ext(rel)
		base := strings.TrimSuffix(rel, ext)
		hashed := base + "." + hexHash + ext

		m.hashes[rel] = hashed
		m.reverse[hashed] = rel
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("building asset manifest: %w", err)
	}

	return m, nil
}

// AssetPath returns the public URL for a logical asset path.
// e.g. AssetPath("css/app.css") → "/static/css/app.a1b2c3d4e5f6.css"
func (m *Manifest) AssetPath(logical string) string {
	if hashed, ok := m.hashes[logical]; ok {
		return "/static/" + hashed
	}
	return "/static/" + logical
}

// Len returns the number of assets in the manifest.
func (m *Manifest) Len() int {
	return len(m.hashes)
}

// Handler returns an http.Handler that serves static files, resolving hashed
// URLs back to real files. Hashed URLs get immutable cache headers; direct
// (unhashed) URLs get short-lived caching.
func (m *Manifest) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlPath := r.URL.Path

		if real, ok := m.reverse[urlPath]; ok {
			// Hashed URL → serve real file with immutable cache.
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			http.ServeFile(w, r, filepath.Join(m.dir, filepath.FromSlash(real)))
			return
		}

		// Direct (unhashed) URL — short cache.
		w.Header().Set("Cache-Control", "public, max-age=60")
		http.ServeFile(w, r, filepath.Join(m.dir, filepath.FromSlash(urlPath)))
	})
}
