package handlers

import (
	"net/http"

	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

var allowedEmojis = map[string]bool{
	"\U0001F44D": true, // 👍
	"\U0001F44E": true, // 👎
	"\U0001F604": true, // 😄
	"\U0001F389": true, // 🎉
	"\U0001F615": true, // 😕
	"❤️":         true, // ❤️
	"\U0001F680": true, // 🚀
	"\U0001F440": true, // 👀
}

type ReactionHandler struct {
	db     *models.DB
	engine *render.Engine
}

func NewReactionHandler(db *models.DB, engine *render.Engine) *ReactionHandler {
	return &ReactionHandler{db: db, engine: engine}
}

// ToggleReaction handles POST /reactions/{targetType}/{targetID}
func (h *ReactionHandler) ToggleReaction(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	targetType := r.PathValue("targetType")
	targetID := r.PathValue("targetID")
	emoji := r.FormValue("emoji")

	if targetType != "ticket" && targetType != "comment" {
		http.Error(w, "Invalid target type", http.StatusBadRequest)
		return
	}

	if !allowedEmojis[emoji] {
		http.Error(w, "Invalid emoji", http.StatusBadRequest)
		return
	}

	if _, err := h.db.ToggleReaction(r.Context(), targetType, targetID, user.ID, emoji); err != nil {
		http.Error(w, "Failed to toggle reaction", http.StatusInternalServerError)
		return
	}

	groups, _ := h.db.ListReactionGroups(r.Context(), targetType, targetID, user.ID)

	_ = h.engine.RenderPartial(w, "reactions.html", map[string]any{
		"TargetType": targetType,
		"TargetID":   targetID,
		"Groups":     groups,
	})
}
