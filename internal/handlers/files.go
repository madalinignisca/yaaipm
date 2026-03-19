package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/madalin/forgedesk/internal/ai"
	"github.com/madalin/forgedesk/internal/config"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/storage"
)

// FileHandler handles file upload, generation, and serving.
type FileHandler struct {
	s3     *storage.S3Client
	db     *models.DB
	gemini *ai.GeminiClient
	cfg    *config.Config
}

// NewFileHandler creates a new file handler.
func NewFileHandler(s3 *storage.S3Client, db *models.DB, gemini *ai.GeminiClient, cfg *config.Config) *FileHandler {
	return &FileHandler{s3: s3, db: db, gemini: gemini, cfg: cfg}
}

// ServeFile proxies a file from S3, setting immutable cache headers.
func (h *FileHandler) ServeFile(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/files/")
	if key == "" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	body, contentType, err := h.s3.Get(r.Context(), key)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	io.Copy(w, body)
}

// UploadImage handles multipart image upload to S3.
func (h *FileHandler) UploadImage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	projectID := r.FormValue("project_id")
	if projectID == "" {
		jsonError(w, "project_id is required", http.StatusBadRequest)
		return
	}

	// Verify project access
	orgID, err := h.checkProjectAccess(r, user, projectID)
	if err != nil {
		jsonError(w, "Access denied", http.StatusForbidden)
		return
	}

	// Parse multipart (max 10MB)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		jsonError(w, "File too large (max 10MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "No file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ct := header.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		jsonError(w, "Only image files are allowed", http.StatusBadRequest)
		return
	}

	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = mimeToExt(ct)
	}

	key := fmt.Sprintf("orgs/%s/projects/%s/images/%s%s", orgID, projectID, uuid.New().String(), ext)

	if err := h.s3.Upload(r.Context(), key, file, ct); err != nil {
		log.Printf("s3 upload error: %v", err)
		jsonError(w, "Upload failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]string{
			"filePath": "/files/" + key,
		},
	})
}

// GenerateImage generates an image via AI and uploads it to S3.
func (h *FileHandler) GenerateImage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Prompt    string `json:"prompt"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Prompt == "" || req.ProjectID == "" {
		jsonError(w, "prompt and project_id are required", http.StatusBadRequest)
		return
	}

	orgID, err := h.checkProjectAccess(r, user, req.ProjectID)
	if err != nil {
		jsonError(w, "Access denied", http.StatusForbidden)
		return
	}

	if h.gemini == nil {
		jsonError(w, "AI not configured", http.StatusServiceUnavailable)
		return
	}

	imgBytes, mimeType, usage, err := h.gemini.GenerateImage(r.Context(), req.Prompt)
	if err != nil {
		log.Printf("image generation error: %v", err)
		jsonError(w, "Image generation failed", http.StatusInternalServerError)
		return
	}

	ext := mimeToExt(mimeType)
	key := fmt.Sprintf("orgs/%s/projects/%s/images/%s%s", orgID, req.ProjectID, uuid.New().String(), ext)

	if err := h.s3.Upload(r.Context(), key, bytes.NewReader(imgBytes), mimeType); err != nil {
		log.Printf("s3 upload error: %v", err)
		jsonError(w, "Upload failed", http.StatusInternalServerError)
		return
	}

	// Record AI usage with image pricing
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		costCents := h.cfg.CalculateAICost(usage.Model, usage.InputTokens, usage.OutputTokens, usage.HasImageOutput)
		if err := h.db.CreateAIUsageEntry(r.Context(), orgID, &req.ProjectID, &user.ID,
			usage.Model, "Image generation", int(usage.InputTokens), int(usage.OutputTokens), costCents); err != nil {
			log.Printf("recording ai usage: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]string{
			"filePath": "/files/" + key,
		},
	})
}

// UploadFile handles general file upload (any type) for ticket attachments.
func (h *FileHandler) UploadFile(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	ticketID := r.FormValue("ticket_id")
	if ticketID == "" {
		jsonError(w, "ticket_id is required", http.StatusBadRequest)
		return
	}

	ticket, err := h.db.GetTicket(r.Context(), ticketID)
	if err != nil {
		jsonError(w, "Ticket not found", http.StatusNotFound)
		return
	}

	orgID, err := h.checkProjectAccess(r, user, ticket.ProjectID)
	if err != nil {
		jsonError(w, "Access denied", http.StatusForbidden)
		return
	}

	// Parse multipart (max 50MB)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		jsonError(w, "File too large (max 50MB)", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "No file provided", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}

	ext := filepath.Ext(header.Filename)
	fileID := uuid.New().String()
	key := fmt.Sprintf("orgs/%s/projects/%s/attachments/%s%s", orgID, ticket.ProjectID, fileID, ext)

	if err := h.s3.Upload(r.Context(), key, file, ct); err != nil {
		log.Printf("s3 upload error: %v", err)
		jsonError(w, "Upload failed", http.StatusInternalServerError)
		return
	}

	filePath := "/files/" + key
	att, err := h.db.CreateAttachment(r.Context(), ticketID, header.Filename, filePath, ct, header.Size, user.ID)
	if err != nil {
		log.Printf("saving attachment record: %v", err)
		jsonError(w, "Failed to record attachment", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"data": map[string]any{
			"id":          att.ID,
			"fileName":    att.FileName,
			"filePath":    att.FilePath,
			"contentType": att.ContentType,
			"sizeBytes":   att.SizeBytes,
		},
	})
}

// DeleteAttachment removes an attachment record after verifying authorization.
func (h *FileHandler) DeleteAttachment(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	attachmentID := r.PathValue("attachmentID")
	if attachmentID == "" {
		jsonError(w, "attachment ID required", http.StatusBadRequest)
		return
	}

	// Fetch attachment to verify authorization
	att, err := h.db.GetAttachmentByID(r.Context(), attachmentID)
	if err != nil {
		jsonError(w, "Attachment not found", http.StatusNotFound)
		return
	}

	// Verify project access via ticket
	ticket, err := h.db.GetTicket(r.Context(), att.TicketID)
	if err != nil {
		jsonError(w, "Ticket not found", http.StatusNotFound)
		return
	}

	if _, err := h.checkProjectAccess(r, user, ticket.ProjectID); err != nil {
		jsonError(w, "Access denied", http.StatusForbidden)
		return
	}

	// Only the uploader or staff can delete
	if att.UploadedBy != user.ID && user.Role != "superadmin" && user.Role != "staff" {
		jsonError(w, "Only the uploader or staff can delete attachments", http.StatusForbidden)
		return
	}

	if err := h.db.DeleteAttachment(r.Context(), attachmentID); err != nil {
		log.Printf("deleting attachment: %v", err)
		jsonError(w, "Failed to delete", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

// checkProjectAccess verifies user access to a project and returns the org ID.
func (h *FileHandler) checkProjectAccess(r *http.Request, user *models.User, projectID string) (string, error) {
	proj, err := h.db.GetProjectByID(r.Context(), projectID)
	if err != nil {
		return "", fmt.Errorf("project not found")
	}
	if user.Role != "superadmin" && user.Role != "staff" {
		if _, err := h.db.GetOrgMembership(r.Context(), user.ID, proj.OrgID); err != nil {
			return "", fmt.Errorf("no access")
		}
	}
	return proj.OrgID, nil
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func mimeToExt(ct string) string {
	switch ct {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "application/zip":
		return ".zip"
	case "application/gzip":
		return ".gz"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	default:
		return ".bin"
	}
}
