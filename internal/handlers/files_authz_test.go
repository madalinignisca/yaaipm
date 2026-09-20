package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/testutil"
)

// ── #159: ServeFile authorization ────────────────────────────────────
//
// Until #159, ServeFile took the S3 key straight from the URL and fetched
// it: no session, no org check, no database lookup at all. The route was
// also registered outside the protected group, so the handler was reachable
// with no cookie. These tests pin the two halves of the fix — who may read
// a file, and what the response permits a cache to do with it.
//
// Note on fixtures: every pre-existing test in files_xss_test.go builds its
// user with role "superadmin", which short-circuits authorizeOrgAccess
// before any membership query runs. That is why none of them would have
// caught this. The users here are deliberately role "client".

// seedOrgFile creates an org + project and seeds one object in the fake
// store under that org's key prefix. It returns the org ID and the key.
func seedOrgFile(t *testing.T, db *models.DB, fake *fakeObjectStore, slug, namespace, fileName, ct, body string) (orgID, key string) {
	t.Helper()
	ctx := context.Background()

	org, err := db.CreateOrg(ctx, slug+" Org", slug)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	proj, err := db.CreateProject(ctx, org.ID, slug+" Proj", slug+"-proj")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	key = "orgs/" + org.ID + "/projects/" + proj.ID + "/" + namespace + "/" + fileName
	fake.seedAttachment(t, key, ct, body)
	return org.ID, key
}

// clientUserIn creates a role-"client" user, optionally a member of orgID.
// Client is the role that actually exercises the membership check; staff
// and superadmin bypass it.
func clientUserIn(t *testing.T, db *models.DB, email, orgID string) *models.User {
	t.Helper()
	ctx := context.Background()

	user, err := db.CreateUser(ctx, email, "x", "Client", "client")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if orgID != "" {
		if err := db.AddOrgMember(ctx, user.ID, orgID, "member"); err != nil {
			t.Fatalf("add org member: %v", err)
		}
	}
	return user
}

// serveFileAs invokes ServeFile with the given user in context (nil user =
// an unauthenticated request reaching the handler).
func serveFileAs(h *FileHandler, user *models.User, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/files/"+key, http.NoBody)
	if user != nil {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, user))
	}
	rec := httptest.NewRecorder()
	h.ServeFile(rec, req)
	return rec
}

// The headline defect: a client of one org could read another org's files.
func TestServeFileDeniesClientFromAnotherOrg(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	_, victimKey := seedOrgFile(t, db, fake, "victim", "attachments", "secret.pdf",
		"application/octet-stream", "CONFIDENTIAL-CLIENT-DATA")
	otherOrgID, _ := seedOrgFile(t, db, fake, "outsider", "attachments", "own.pdf",
		"application/octet-stream", "harmless")

	intruder := clientUserIn(t, db, "intruder@test.com", otherOrgID)

	rec := serveFileAs(h, intruder, victimKey)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 — a client read another org's file", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "CONFIDENTIAL-CLIENT-DATA") {
		t.Error("handler served another org's file content")
	}
}

// The fix must not lock out the people the file belongs to.
func TestServeFileServesFileToMemberOfOwningOrg(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	orgID, key := seedOrgFile(t, db, fake, "owner", "attachments", "brief.pdf",
		"application/octet-stream", "LEGITIMATE-CONTENT")
	member := clientUserIn(t, db, "member@test.com", orgID)

	rec := serveFileAs(h, member, key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a member of the owning org was denied", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "LEGITIMATE-CONTENT") {
		t.Error("member did not receive the file body")
	}
}

// Defense in depth: the route now sits behind AuthMiddleware, but the
// handler must not serve anything if it is ever re-registered outside it.
func TestServeFileDeniesUnauthenticatedRequest(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	_, key := seedOrgFile(t, db, fake, "anon", "attachments", "secret.pdf",
		"application/octet-stream", "CONFIDENTIAL-CLIENT-DATA")

	rec := serveFileAs(h, nil, key)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a request with no user", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "CONFIDENTIAL-CLIENT-DATA") {
		t.Error("handler served a file to an unauthenticated request")
	}
}

// A key that carries no org segment cannot be authorized against anything,
// so it must be refused rather than fetched. Seeded deliberately, so the
// 404 proves refusal and not merely a store miss.
func TestServeFileRejectsKeyWithoutOrgSegment(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	orgID, _ := seedOrgFile(t, db, fake, "legit", "attachments", "a.pdf",
		"application/octet-stream", "x")
	user := clientUserIn(t, db, "user@test.com", orgID)

	fake.seedAttachment(t, "legacy/loose-object.pdf", "application/octet-stream", "UNSCOPED-CONTENT")

	rec := serveFileAs(h, user, "legacy/loose-object.pdf")

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a key with no orgs/<id>/ prefix", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "UNSCOPED-CONTENT") {
		t.Error("handler served an object whose key could not be authorized")
	}
}

// Client files must never be storable by a shared cache. The zone is
// Cloudflare-proxied and attachment keys end in .pdf/.docx/.zip — all in
// Cloudflare's default cacheable extension list — so "public" here means
// client documents are copied to edge nodes, served from there without the
// origin (and therefore without the new auth check) ever being consulted,
// and left behind for a year after the attachment is deleted.
func TestServeFileNeverMarksClientFilesPubliclyCacheable(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	orgID, attachmentKey := seedOrgFile(t, db, fake, "cache", "attachments", "doc.pdf",
		"application/octet-stream", "body")
	member := clientUserIn(t, db, "cache@test.com", orgID)

	// Same org, image namespace — served inline, still not shared-cacheable.
	imageKey := strings.Replace(attachmentKey, "/attachments/doc.pdf", "/images/pic.png", 1)
	fake.seedAttachment(t, imageKey, "image/png", "pngbody")

	for _, tc := range []struct{ name, key, want string }{
		{"attachment", attachmentKey, "private, no-store"},
		{"inline image", imageKey, "private, max-age=31536000, immutable"},
	} {
		rec := serveFileAs(h, member, tc.key)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", tc.name, rec.Code)
		}
		cc := rec.Header().Get("Cache-Control")
		if strings.Contains(cc, "public") {
			t.Errorf("%s: Cache-Control = %q — client files must never be shared-cacheable", tc.name, cc)
		}
		if cc != tc.want {
			t.Errorf("%s: Cache-Control = %q, want %q", tc.name, cc, tc.want)
		}
	}
}

// projectIDOf pulls the project segment out of an object key, so tests can
// act on the project without the seed helper returning four values.
func projectIDOf(t *testing.T, key string) string {
	t.Helper()
	parts := strings.Split(key, "/")
	if len(parts) < 4 {
		t.Fatalf("key %q has no project segment", key)
	}
	return parts[3]
}

// Authorization must follow the project's CURRENT owner, not the org that
// happened to own it when the file was uploaded.
//
// S3 keys embed the org ID at upload time and are never rewritten, but
// TransferProject (internal/models/queries.go, wired to a staff-only route
// in cmd/server/main.go) moves a project between orgs with a single UPDATE.
// Anchoring on the key's org segment therefore fails in both directions at
// once: the org the project was deliberately moved AWAY from keeps reading
// every file, and the org that now owns it cannot read any of them.
//
// Rewriting the keys on transfer is not a fix — it is expensive, not
// atomic, and URLs already embedded in stored markdown would still point
// at the old key. The project segment is the stable identifier; the owning
// org is resolved from the database, where it is actually true.
func TestServeFileFollowsProjectOwnershipAfterTransfer(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}
	ctx := context.Background()

	orgA, key := seedOrgFile(t, db, fake, "transfer-from", "attachments", "brief.pdf",
		"application/octet-stream", "TRANSFERRED-CONTENT")
	projectID := projectIDOf(t, key)

	orgB, _ := seedOrgFile(t, db, fake, "transfer-to", "attachments", "unrelated.pdf",
		"application/octet-stream", "other")

	oldOwner := clientUserIn(t, db, "old-owner@test.com", orgA)
	newOwner := clientUserIn(t, db, "new-owner@test.com", orgB)

	if err := db.TransferProject(ctx, projectID, orgB); err != nil {
		t.Fatalf("transfer project: %v", err)
	}

	if rec := serveFileAs(h, newOwner, key); rec.Code != http.StatusOK {
		t.Errorf("new owner: status = %d, want 200 — the org that owns the project now cannot read its files", rec.Code)
	}

	rec := serveFileAs(h, oldOwner, key)
	if rec.Code != http.StatusNotFound {
		t.Errorf("former owner: status = %d, want 404 — the org the project was moved away from can still read its files", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "TRANSFERRED-CONTENT") {
		t.Error("former owner still received the file body after the project was transferred away")
	}
}

// Kills the mutant that survived review: resolving "the first UUID anywhere
// in the key" rather than requiring the orgs/<id>/projects/<id>/ shape. Not
// exploitable today — S3 key matching is exact and only correctly shaped
// keys are ever written — but the shape is what makes that true, so it
// should be pinned rather than left as an accident.
func TestServeFileRejectsKeyWithMisplacedProjectSegment(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	_, key := seedOrgFile(t, db, fake, "shape", "attachments", "real.pdf",
		"application/octet-stream", "x")
	orgID := strings.Split(key, "/")[1]
	projectID := projectIDOf(t, key)
	member := clientUserIn(t, db, "shape@test.com", orgID)

	// The UUIDs the member is genuinely entitled to, positioned exactly
	// where a shape-blind parser would look for them, but under literal
	// segments the application never writes. Each case kills a different
	// way of relaxing the shape check.
	for _, tc := range []struct{ name, key string }{
		{"wrong root segment", "stray/" + orgID + "/projects/" + projectID + "/attachments/a.pdf"},
		{"wrong projects segment", "orgs/" + orgID + "/decoys/" + projectID + "/attachments/b.pdf"},
	} {
		fake.seedAttachment(t, tc.key, "application/octet-stream", "MISSHAPEN-CONTENT")

		rec := serveFileAs(h, member, tc.key)

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 for a key that is not orgs/<id>/projects/<id>/...", tc.name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "MISSHAPEN-CONTENT") {
			t.Errorf("%s: handler served an object whose key shape it cannot authorize", tc.name)
		}
	}
}

// Refusals must not be storable by a shared cache. Cloudflare's documented
// default for a response with no Cache-Control is to cache a 303 for 20
// minutes and a 404 for 3, and every attachment URL ends in an extension
// on its default cacheable list. One anonymous request against a leaked
// URL would otherwise pin a redirect at the edge and bounce legitimate
// members to /login for twenty minutes.
func TestServeFileRefusalsAreNotCacheable(t *testing.T) {
	pool := testutil.SetupTestDB(t)
	db := models.NewDB(pool)
	fake := newFakeObjectStore()
	h := &FileHandler{s3: fake, db: db}

	_, key := seedOrgFile(t, db, fake, "refusal", "attachments", "doc.pdf",
		"application/octet-stream", "body")
	outsiderOrg, _ := seedOrgFile(t, db, fake, "refusal-other", "attachments", "o.pdf",
		"application/octet-stream", "y")
	outsider := clientUserIn(t, db, "refusal@test.com", outsiderOrg)

	for _, tc := range []struct {
		name string
		user *models.User
		key  string
	}{
		{"unauthenticated", nil, key},
		{"cross-tenant", outsider, key},
		{"malformed key", outsider, "not-a-valid-key.pdf"},
	} {
		rec := serveFileAs(h, tc.user, tc.key)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", tc.name, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want %q — a cached refusal locks the URL for everyone", tc.name, cc, "no-store")
		}
	}
}
