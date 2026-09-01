package static

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// computeExpectedHash returns the hashed filename for a given relative path and content,
// matching the logic in NewManifest.
func computeExpectedHash(rel string, content []byte) string {
	hash := sha256.Sum256(content)
	hexHash := fmt.Sprintf("%x", hash[:6])
	ext := filepath.Ext(rel)
	base := strings.TrimSuffix(rel, ext)
	return base + "." + hexHash + ext
}

func TestNewManifest(t *testing.T) {
	dir := t.TempDir()

	// Create a CSS file.
	cssDir := filepath.Join(dir, "css")
	if err := os.MkdirAll(cssDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cssContent := []byte("body { color: red; }")
	if err := os.WriteFile(filepath.Join(cssDir, "app.css"), cssContent, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a JS file.
	jsContent := []byte("console.log('hello');")
	if err := os.WriteFile(filepath.Join(dir, "main.js"), jsContent, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}

	if m.Len() != 2 {
		t.Errorf("Len() = %d, want 2", m.Len())
	}

	// Verify AssetPath returns hashed paths for known files.
	cssHashed := computeExpectedHash("css/app.css", cssContent)
	got := m.AssetPath("css/app.css")
	want := "/static/" + cssHashed
	if got != want {
		t.Errorf("AssetPath(css/app.css) = %q, want %q", got, want)
	}

	jsHashed := computeExpectedHash("main.js", jsContent)
	got = m.AssetPath("main.js")
	want = "/static/" + jsHashed
	if got != want {
		t.Errorf("AssetPath(main.js) = %q, want %q", got, want)
	}
}

func TestAssetPathUnknownFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "exists.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}

	got := m.AssetPath("unknown.js")
	want := "/static/unknown.js"
	if got != want {
		t.Errorf("AssetPath(unknown.js) = %q, want %q", got, want)
	}
}

func TestContentHashChanges(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "style.css")

	// First version.
	content1 := []byte("body { color: blue; }")
	if err := os.WriteFile(filePath, content1, 0o644); err != nil {
		t.Fatal(err)
	}
	m1, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}
	path1 := m1.AssetPath("style.css")

	// Second version with different content.
	content2 := []byte("body { color: green; }")
	err = os.WriteFile(filePath, content2, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}
	path2 := m2.AssetPath("style.css")

	if path1 == path2 {
		t.Errorf("AssetPath should differ when content changes, both returned %q", path1)
	}

	// Same content again should produce the same hash.
	err = os.WriteFile(filePath, content1, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	m3, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}
	path3 := m3.AssetPath("style.css")

	if path3 != path1 {
		t.Errorf("same content should produce same hash: got %q, want %q", path3, path1)
	}
}

func TestHandlerHashedURL(t *testing.T) {
	dir := t.TempDir()
	content := []byte("body { margin: 0; }")
	if err := os.WriteFile(filepath.Join(dir, "app.css"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}

	handler := m.Handler()

	// Get the hashed path (strip the "/static/" prefix since the handler receives the path without it).
	hashedPath := strings.TrimPrefix(m.AssetPath("app.css"), "/static/")

	req := httptest.NewRequest(http.MethodGet, "/"+hashedPath, http.NoBody)
	req.URL.Path = hashedPath
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "31536000") {
		t.Errorf("Cache-Control = %q, want immutable with max-age=31536000", cc)
	}

	if rec.Body.String() != string(content) {
		t.Errorf("body = %q, want %q", rec.Body.String(), string(content))
	}
}

func TestHandlerUnhashedURL(t *testing.T) {
	dir := t.TempDir()
	content := []byte("console.log('test');")
	if err := os.WriteFile(filepath.Join(dir, "script.js"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}

	handler := m.Handler()

	// Request the file by its logical (unhashed) name.
	req := httptest.NewRequest(http.MethodGet, "/script.js", http.NoBody)
	req.URL.Path = "script.js"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "max-age=60") {
		t.Errorf("Cache-Control = %q, want max-age=60", cc)
	}
	if strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, should not contain immutable for unhashed URLs", cc)
	}
}

func TestNewManifestNonexistentDir(t *testing.T) {
	_, err := NewManifest("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("NewManifest() with nonexistent dir should return error")
	}
}

func TestNewManifestEmptyDir(t *testing.T) {
	dir := t.TempDir()

	m, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}

	if m.Len() != 0 {
		t.Errorf("Len() = %d, want 0 for empty dir", m.Len())
	}

	got := m.AssetPath("anything.js")
	want := "/static/anything.js"
	if got != want {
		t.Errorf("AssetPath on empty manifest = %q, want %q", got, want)
	}
}

func TestNewManifestNestedDirs(t *testing.T) {
	dir := t.TempDir()

	// Create nested directory structure.
	nested := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("deeply nested")
	if err := os.WriteFile(filepath.Join(nested, "deep.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := NewManifest(dir)
	if err != nil {
		t.Fatalf("NewManifest() error: %v", err)
	}

	if m.Len() != 1 {
		t.Errorf("Len() = %d, want 1", m.Len())
	}

	got := m.AssetPath("a/b/c/deep.txt")
	expectedHash := computeExpectedHash("a/b/c/deep.txt", content)
	want := "/static/" + expectedHash
	if got != want {
		t.Errorf("AssetPath(a/b/c/deep.txt) = %q, want %q", got, want)
	}
}
