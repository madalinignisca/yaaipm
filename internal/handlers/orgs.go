package handlers

import (
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/mail"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type OrgHandler struct {
	db      *models.DB
	engine  *render.Engine
	mailer  *mail.Mailer
	baseURL string
}

func NewOrgHandler(db *models.DB, engine *render.Engine, mailer *mail.Mailer, baseURL string) *OrgHandler {
	return &OrgHandler{db: db, engine: engine, mailer: mailer, baseURL: baseURL}
}

var slugRegex = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	slug := strings.ToLower(strings.TrimSpace(s))
	slug = slugRegex.ReplaceAllString(slug, "-")
	return strings.Trim(slug, "-")
}

func (h *OrgHandler) OrgPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	slug := r.PathValue("orgSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Organization not found")
		return
	}

	// Check access
	if !auth.IsStaffOrAbove(user.Role) {
		_, err := h.db.GetOrgMembership(r.Context(), user.ID, org.ID)
		if err != nil {
			h.engine.RenderError(w, http.StatusForbidden, "Access denied")
			return
		}
	}

	projects, err := h.db.ListProjects(r.Context(), org.ID)
	if err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "Failed to load projects")
		return
	}

	var orgs []models.Organization
	if auth.IsStaffOrAbove(user.Role) {
		orgs, _ = h.db.ListAllOrgs(r.Context())
	} else {
		orgs, _ = h.db.ListUserOrgs(r.Context(), user.ID)
	}

	h.engine.Render(w, "dashboard.html", render.PageData{
		Title:       org.Name,
		User:        user,
		Org:         org,
		Orgs:        orgs,
		CurrentPath: r.URL.Path,
		Data:        projects,
	})
}

func (h *OrgHandler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	slug := slugify(name)

	org, err := h.db.CreateOrg(r.Context(), name, slug)
	if err != nil {
		log.Printf("creating org: %v", err)
		http.Error(w, "Failed to create organization", http.StatusInternalServerError)
		return
	}

	// Make creator the owner
	if err := h.db.AddOrgMember(r.Context(), user.ID, org.ID, auth.OrgRoleOwner); err != nil {
		log.Printf("adding org member: %v", err)
	}

	http.Redirect(w, r, "/orgs/"+org.Slug, http.StatusSeeOther)
}

func (h *OrgHandler) OrgSettings(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	slug := r.PathValue("orgSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if err != nil {
		h.engine.RenderError(w, http.StatusNotFound, "Organization not found")
		return
	}

	members, err := h.db.ListOrgMembers(r.Context(), org.ID)
	if err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "Failed to load members")
		return
	}

	canManage := auth.IsStaffOrAbove(user.Role)
	if !canManage {
		if m, err := h.db.GetOrgMembership(r.Context(), user.ID, org.ID); err == nil {
			canManage = auth.CanManageOrg(m.Role)
		}
	}

	var orgs []models.Organization
	if auth.IsStaffOrAbove(user.Role) {
		orgs, _ = h.db.ListAllOrgs(r.Context())
	} else {
		orgs, _ = h.db.ListUserOrgs(r.Context(), user.ID)
	}

	invitations, _ := h.db.ListOrgInvitations(r.Context(), org.ID)

	h.engine.Render(w, "org_settings.html", render.PageData{
		Title:       org.Name + " Settings",
		User:        user,
		Org:         org,
		Orgs:        orgs,
		CurrentPath: r.URL.Path,
		Data: map[string]any{
			"Members":       members,
			"Invitations":   invitations,
			"CanManage":     canManage,
			"CurrentUserID": user.ID,
			"OrgSlug":       org.Slug,
		},
	})
}

// canManageOrgMembers checks if the current user has permission to manage members of the given org.
func (h *OrgHandler) canManageOrgMembers(r *http.Request, user *models.User, orgID string) bool {
	if auth.IsStaffOrAbove(user.Role) {
		return true
	}
	m, err := h.db.GetOrgMembership(r.Context(), user.ID, orgID)
	if err != nil {
		return false
	}
	return auth.CanManageOrg(m.Role)
}

func (h *OrgHandler) renderMemberList(w http.ResponseWriter, r *http.Request, org *models.Organization, user *models.User) {
	members, err := h.db.ListOrgMembers(r.Context(), org.ID)
	if err != nil {
		http.Error(w, "Failed to load members", http.StatusInternalServerError)
		return
	}
	canManage := h.canManageOrgMembers(r, user, org.ID)
	h.engine.RenderPartial(w, "member_list.html", map[string]any{
		"Members":       members,
		"CanManage":     canManage,
		"CurrentUserID": user.ID,
		"OrgSlug":       org.Slug,
	})
}

func (h *OrgHandler) InviteMember(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	slug := r.PathValue("orgSlug")

	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if !h.canManageOrgMembers(r, user, org.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	email := strings.TrimSpace(r.FormValue("email"))
	role := r.FormValue("role")
	if email == "" || role == "" {
		http.Error(w, "Email and role are required", http.StatusBadRequest)
		return
	}
	if role != auth.OrgRoleMember && role != auth.OrgRoleAdmin {
		http.Error(w, "Invalid role", http.StatusBadRequest)
		return
	}

	// Check not already a member
	if target, err := h.db.GetUserByEmail(r.Context(), email); err == nil {
		if _, err := h.db.GetOrgMembership(r.Context(), target.ID, org.ID); err == nil {
			http.Error(w, "User is already a member of this organization", http.StatusConflict)
			return
		}
	}

	// Check no existing pending invitation
	hasPending, err := h.db.HasPendingInvitation(r.Context(), email, org.ID)
	if err != nil {
		log.Printf("checking pending invitation: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if hasPending {
		http.Error(w, "An invitation is already pending for this email", http.StatusConflict)
		return
	}

	// Generate token
	rawToken, tokenHash, err := generateInviteToken()
	if err != nil {
		log.Printf("generating invite token: %v", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	expiresAt := time.Now().Add(7 * 24 * time.Hour) // 7 days
	_, err = h.db.CreateInvitation(r.Context(), email, org.ID, role, tokenHash, user.ID, expiresAt)
	if err != nil {
		log.Printf("creating invitation: %v", err)
		http.Error(w, "Failed to create invitation", http.StatusInternalServerError)
		return
	}

	inviteURL := h.baseURL + "/invite/" + rawToken

	// Send email (best-effort)
	if h.mailer.IsEnabled() {
		if err := h.mailer.SendInvitation(email, org.Name, user.Name, inviteURL); err != nil {
			log.Printf("sending invite email: %v", err)
		}
	}

	h.engine.RenderPartial(w, "invite_result.html", map[string]any{
		"InviteURL":    inviteURL,
		"Email":        email,
		"EmailEnabled": h.mailer.IsEnabled(),
	})
}

func (h *OrgHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	slug := r.PathValue("orgSlug")
	targetID := r.PathValue("userID")

	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if !h.canManageOrgMembers(r, user, org.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if targetID == user.ID {
		http.Error(w, "You cannot remove yourself", http.StatusBadRequest)
		return
	}

	// Check if target is the last owner
	targetMembership, err := h.db.GetOrgMembership(r.Context(), targetID, org.ID)
	if err != nil {
		http.Error(w, "Member not found", http.StatusNotFound)
		return
	}
	if targetMembership.Role == auth.OrgRoleOwner {
		count, err := h.db.CountOrgOwners(r.Context(), org.ID)
		if err != nil || count <= 1 {
			http.Error(w, "Cannot remove the last owner", http.StatusBadRequest)
			return
		}
	}

	if err := h.db.RemoveOrgMember(r.Context(), targetID, org.ID); err != nil {
		log.Printf("removing org member: %v", err)
		http.Error(w, "Failed to remove member", http.StatusInternalServerError)
		return
	}

	h.renderMemberList(w, r, org, user)
}

func (h *OrgHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	slug := r.PathValue("orgSlug")
	targetID := r.PathValue("userID")

	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	if !h.canManageOrgMembers(r, user, org.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	newRole := r.FormValue("role")
	if newRole != auth.OrgRoleOwner && newRole != auth.OrgRoleAdmin && newRole != auth.OrgRoleMember {
		http.Error(w, "Invalid role", http.StatusBadRequest)
		return
	}

	// Prevent demoting the last owner
	currentMembership, err := h.db.GetOrgMembership(r.Context(), targetID, org.ID)
	if err != nil {
		http.Error(w, "Member not found", http.StatusNotFound)
		return
	}
	if currentMembership.Role == auth.OrgRoleOwner && newRole != auth.OrgRoleOwner {
		count, err := h.db.CountOrgOwners(r.Context(), org.ID)
		if err != nil || count <= 1 {
			http.Error(w, "Cannot demote the last owner", http.StatusBadRequest)
			return
		}
	}

	if err := h.db.UpdateOrgMemberRole(r.Context(), targetID, org.ID, newRole); err != nil {
		log.Printf("updating org member role: %v", err)
		http.Error(w, "Failed to update role", http.StatusInternalServerError)
		return
	}

	h.renderMemberList(w, r, org, user)
}
