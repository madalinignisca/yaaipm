package handlers

import (
	"log"
	"net/http"
	"strings"

	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type CommentHandler struct {
	db     *models.DB
	engine *render.Engine
}

func NewCommentHandler(db *models.DB, engine *render.Engine) *CommentHandler {
	return &CommentHandler{db: db, engine: engine}
}

func (h *CommentHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	ticketID := r.PathValue("ticketID")
	body := strings.TrimSpace(r.FormValue("body"))

	if body == "" {
		http.Error(w, "Comment body is required", http.StatusBadRequest)
		return
	}

	comment, err := h.db.CreateComment(r.Context(), ticketID, &user.ID, nil, body)
	if err != nil {
		log.Printf("creating comment: %v", err)
		http.Error(w, "Failed to post comment", http.StatusInternalServerError)
		return
	}

	_ = h.db.CreateActivity(r.Context(), ticketID, &user.ID, nil, "comment", "{}")

	// Return the new comment as an HTMX partial (no reactions yet on a brand-new comment)
	if err := h.engine.RenderPartial(w, "comment.html", map[string]any{
		"Comment":          comment,
		"UserName":         user.Name,
		"CommentReactions": []models.ReactionGroup(nil),
	}); err != nil {
		log.Printf("rendering comment partial: %v", err)
	}
}
