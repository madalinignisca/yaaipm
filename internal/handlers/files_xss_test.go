package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/testutil"
)

// ── fakeObjectStore ───────────────────────────────────────────────────
//
// In-memory objectStore used by #24 tests to inject known content and
// observe Delete calls, without needing a live S3 endpoint.

type fakeObject struct {
	body        []byte
	contentType string
}

type fakeObjectStore struct {
	mu         sync.Mutex
	objects    map[string]fakeObject
	deleted    []string
	failDelete bool
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: make(map[string]fakeObject)}
}

func (f *fakeObjectStore) Upload(_ context.Context, key string, data io.Reader, ct string) error {
	buf, err := io.ReadAll(data)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = fakeObject{body: buf, contentType: ct}
	return nil
}

func (f *fakeObjectStore) Get(_ context.Context, key string) (io.ReadCloser, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[key]
	if !ok {
		return nil, "", errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(obj.body)), obj.contentType, nil
}

func (f *fakeObjectStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete {
		return errors.New("simulated s3 failure")
	}
	f.deleted = append(f.deleted, key)
	delete(f.objects, key)
	return nil
}

// seedAttachment is a convenience for tests that need a file in the store.
func (f *fakeObjectStore) seedAttachment(t *testing.T, key, contentType, body string) {
	t.Helper()
	if err := f.Upload(context.Background(), key, bytes.NewReader([]byte(body)), contentType); err != nil {
		t.Fatalf("seed attachment: %v", err)
	}
}

// ── ServeFile behavior tests ──────────────────────────────────────────

func callServeFile(h *FileHandler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/files/"+key, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeFile(rec, req)
	return rec
}

// TestServeFileForcesDownloadForHTMLAttachment — the core XSS cut point.
// An attacker uploads HTML into the attachments namespace with a
// script-capable content type. ServeFile must force the browser to
// download it instead of rendering, and must strip the stored CT.
func TestServeFileForcesDownloadForHTMLAttachment(t *testing.T) {
	fake := newFakeObjectStore()
	key := "orgs/o1/projects/p1/attachments/" + "deadbeef.html"
	fake.seedAttachment(t, key, "text/html", "<script>alert('xss')</script>")

	h := &FileHandler{s3: fake}
	rec := callServeFile(h, key)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want 'attachment' prefix", cd)
	}
	if ns := rec.Header().Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", ns)
	}
}

// TestServeFileForcesDownloadForSVGAttachment — SVG is script-capable.
// Even when the path is /attachments/*, SVG must not be served inline.
func TestServeFileForcesDownloadForSVGAttachment(t *testing.T) {
	fake := newFakeObjectStore()
	key := "orgs/o1/projects/p1/attachments/logo.svg"
	fake.seedAttachment(t, key, "image/svg+xml", "<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")

	h := &FileHandler{s3: fake}
	rec := callServeFile(h, key)

	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Error("Content-Disposition should force download")
	}
}

// TestServeFileForcesDownloadForSVGInImagesPath — even inside the
// /images/ namespace (where PNG/JPEG/GIF/WEBP get served inline),
// SVG must NOT be served inline because it can contain script.
func TestServeFileForcesDownloadForSVGInImagesPath(t *testing.T) {
	fake := newFakeObjectStore()
	key := "orgs/o1/projects/p1/images/chart.svg"
	fake.seedAttachment(t, key, "image/svg+xml", "<svg><script>alert(1)</script></svg>")

	h := &FileHandler{s3: fake}
	rec := callServeFile(h, key)

	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream (SVG never inline)", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Error("Content-Disposition should force download for SVG in images path")
	}
}

// TestServeFileRendersSafeImageInline — the legitimate path: a PNG
// in the /images/ namespace with a safe stored CT must still be
// served inline so the rich-text editor can render it in <img>.
func TestServeFileRendersSafeImageInline(t *testing.T) {
	fake := newFakeObjectStore()
	key := "orgs/o1/projects/p1/images/photo.png"
	fake.seedAttachment(t, key, "image/png", "\x89PNG\r\n\x1a\nfake")

	h := &FileHandler{s3: fake}
	rec := callServeFile(h, key)

	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); strings.HasPrefix(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want no 'attachment' for inline images", cd)
	}
	if ns := rec.Header().Get("X-Content-Type-Options"); ns != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff on all responses", ns)
	}
}

// TestServeFileIgnoresUntrustedStoredCT — even if the stored content type
// claims "image/png" but the file is in the attachments namespace, the
// serve path must force download. Path decides, not content type.
func TestServeFileIgnoresUntrustedStoredCT(t *testing.T) {
	fake := newFakeObjectStore()
	key := "orgs/o1/projects/p1/attachments/fake.png"
	fake.seedAttachment(t, key, "image/png", "<script>alert('xss')</script>")

	h := &FileHandler{s3: fake}
	rec := callServeFile(h, key)

	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("attachments path must force octet-stream regardless of stored CT, got %q", got)
	}
}

// TestServeFileReturnsNotFoundForMissingKey is a sanity check that the
// force-download logic does not accidentally turn 404s into 200s.
func TestServeFileReturnsNotFoundForMissingKey(t *testing.T) {
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake}
	rec := callServeFile(h, "orgs/o1/projects/p1/attachments/missing.bin")

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// TestServeFileRejectsPathTraversal — defense in depth for #24. Even if
// a future code path (or a router misconfiguration) let a key with ".."
// reach ServeFile, we must reject it explicitly. To prove the defense
// is real and not just an artifact of the store saying "not found", we
// seed a fake object at the literal traversal-shaped key and verify
// that ServeFile still rejects it WITHOUT serving the content.
func TestServeFileRejectsPathTraversal(t *testing.T) {
	fake := newFakeObjectStore()
	// Seed an object at a literal "..-shaped" key. S3 keys are opaque
	// strings so this is legal at the storage layer — the point of
	// the handler check is to refuse to forward such keys.
	traversalKey := "orgs/o1/../../secret.bin"
	fake.seedAttachment(t, traversalKey, "application/octet-stream", "SECRET")

	h := &FileHandler{s3: fake}

	req := httptest.NewRequest(http.MethodGet, "/files/"+traversalKey, http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeFile(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for path-traversal-shaped key, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Error("handler served a traversal-shaped key — defense not in place")
	}
}

// ── DeleteAttachment S3-cleanup test ─────────────────────────────────

// seedAttachmentFixture creates org + project + ticket + attachment row
// directly in the DB and seeds a matching object in the fake store. It
// returns the created user, attachment row, and S3 key.
func seedAttachmentFixture(t *testing.T, db *models.DB, fake *fakeObjectStore, email, orgSlug, projSlug, fileName string) (*models.User, *models.TicketAttachment, string) {
	t.Helper()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, email, "x", "Owner", "superadmin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	org, err := db.CreateOrg(ctx, orgSlug+" Org", orgSlug)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, projSlug+" Proj", projSlug)
	if err != nil {
		t.Fatalf("create proj: %v", err)
	}
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "t",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err = db.CreateTicket(ctx, ticket); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	key := "orgs/" + org.ID + "/projects/" + proj.ID + "/attachments/" + fileName
	att, err := db.CreateAttachment(ctx, ticket.ID, fileName, "/files/"+key, "application/octet-stream", 42, user.ID)
	if err != nil {
		t.Fatalf("create attachment row: %v", err)
	}
	fake.seedAttachment(t, key, "application/octet-stream", "binary-body")
	return user, att, key
}

// callDeleteAttachment constructs a request with the user in context and
// the attachmentID path value set, then invokes the handler directly,
// bypassing the chi router (and its auth middleware).
func callDeleteAttachment(h *FileHandler, user *models.User, attachmentID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/attachments/"+attachmentID, http.NoBody)
	req.SetPathValue("attachmentID", attachmentID)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, user))
	rec := httptest.NewRecorder()
	h.DeleteAttachment(rec, req)
	return rec
}

// TestDeleteAttachmentRemovesS3Object locks in the second half of #24:
// when an attachment row is removed, the backing S3 object must also
// be deleted so the /files/* URL stops resolving.
func TestDeleteAttachmentRemovesS3Object(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	user, att, key := seedAttachmentFixture(t, db, fake, "owner@test.com", "owner-org", "owner-proj", "abc.bin")

	rec := callDeleteAttachment(h, user, att.ID)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != key {
		t.Errorf("expected S3 delete of key %q, got %v", key, fake.deleted)
	}
	if _, err := db.GetAttachmentByID(context.Background(), att.ID); err == nil {
		t.Error("attachment row should have been deleted from DB")
	}
}

// TestDeleteAttachmentSkipsS3WhenPathHasNoFilesPrefix — defense in depth
// for #24. If a future code path stored an attachment row with a FilePath
// that doesn't start with /files/ (e.g. an external URL), DeleteAttachment
// must not try to pass a malformed key to the S3 client. The DB row
// should still be removed so the handler is idempotent.
func TestDeleteAttachmentSkipsS3WhenPathHasNoFilesPrefix(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "odd@test.com", "x", "Odd", "superadmin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	org, _ := db.CreateOrg(ctx, "Odd Org", "odd-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Odd Proj", "odd-proj")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "t",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err = db.CreateTicket(ctx, ticket); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	// Anomalous path — no /files/ prefix. Simulates legacy/external data.
	att, err := db.CreateAttachment(ctx, ticket.ID, "ext.bin", "external://weird",
		"application/octet-stream", 1, user.ID)
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}

	rec := callDeleteAttachment(h, user, att.ID)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(fake.deleted) != 0 {
		t.Errorf("no S3 delete should be attempted for non-/files/ paths, got %v", fake.deleted)
	}
	if _, err := db.GetAttachmentByID(ctx, att.ID); err == nil {
		t.Error("DB row should still be removed even when no S3 object is tied to it")
	}
}

// TestDeleteAttachmentSkipsS3WhenKeyIsEmpty — if a row has exactly
// "/files/" as its path (degenerate/corrupt state), CutPrefix returns an
// empty key and we must not pass "" to the S3 client.
func TestDeleteAttachmentSkipsS3WhenKeyIsEmpty(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}
	ctx := context.Background()

	user, err := db.CreateUser(ctx, "empty@test.com", "x", "Empty", "superadmin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	org, _ := db.CreateOrg(ctx, "Empty Org", "empty-org")
	proj, _ := db.CreateProject(ctx, org.ID, "Empty Proj", "empty-proj")
	ticket := &models.Ticket{
		ProjectID: proj.ID, Type: "task", Title: "t",
		Status: "backlog", Priority: "medium", CreatedBy: user.ID,
	}
	if err = db.CreateTicket(ctx, ticket); err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	att, err := db.CreateAttachment(ctx, ticket.ID, "empty.bin", "/files/",
		"application/octet-stream", 0, user.ID)
	if err != nil {
		t.Fatalf("create attachment: %v", err)
	}

	rec := callDeleteAttachment(h, user, att.ID)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(fake.deleted) != 0 {
		t.Errorf("no S3 delete should be attempted for an empty key, got %v", fake.deleted)
	}
}

// TestDeleteAttachmentBailsOnS3Failure — if the S3 Delete fails, the
// handler must return an error and leave the DB row intact so the user
// can retry. Otherwise we'd orphan unreachable metadata.
func TestDeleteAttachmentBailsOnS3Failure(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	fake.failDelete = true
	h := &FileHandler{s3: fake, db: db}

	user, att, _ := seedAttachmentFixture(t, db, fake, "owner2@test.com", "owner2-org", "owner2-proj", "bad.bin")

	rec := callDeleteAttachment(h, user, att.ID)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 on S3 failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := db.GetAttachmentByID(context.Background(), att.ID); err != nil {
		t.Errorf("attachment row should still exist after S3 failure: %v", err)
	}
}
