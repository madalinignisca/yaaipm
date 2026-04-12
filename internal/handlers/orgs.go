package handlers

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/madalin/forgedesk/internal/auth"
	"github.com/madalin/forgedesk/internal/mail"
	"github.com/madalin/forgedesk/internal/middleware"
	"github.com/madalin/forgedesk/internal/models"
	"github.com/madalin/forgedesk/internal/render"
)

type OrgHandler struct {
	db                   *models.DB
	engine               *render.Engine
	sessions             *auth.SessionStore
	mailer               *mail.Mailer
	baseURL              string
	protectedSuperadmins []string
}

func NewOrgHandler(db *models.DB, engine *render.Engine, sessions *auth.SessionStore, mailer *mail.Mailer, baseURL string, protectedSuperadmins []string) *OrgHandler {
	return &OrgHandler{db: db, engine: engine, sessions: sessions, mailer: mailer, baseURL: baseURL, protectedSuperadmins: protectedSuperadmins}
}

// isProtectedSuperadmin checks if an email is in the protected superadmins list.
func (h *OrgHandler) isProtectedSuperadmin(email string) bool {
	lower := strings.ToLower(email)
	return slices.Contains(h.protectedSuperadmins, lower)
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

	projects := middleware.GetProjects(r)
	if projects == nil {
		projects, _ = h.db.ListProjects(r.Context(), org.ID)
	}

	_ = h.engine.Render(w, r, "dashboard.html", render.PageData{
		Title:       org.Name,
		User:        user,
		Org:         org,
		Orgs:        middleware.GetOrgs(r),
		Projects:    projects,
		CurrentPath: r.URL.Path,
		Data:        projects,
	})
}

func (h *OrgHandler) SwitchOrg(w http.ResponseWriter, r *http.Request) {
	sess := middleware.GetSession(r)
	orgID := r.FormValue("org_id")
	if orgID == "" {
		http.Error(w, "org_id required", http.StatusBadRequest)
		return
	}

	// Validate the user has access to this org
	orgs := middleware.GetOrgs(r)
	var target *models.Organization
	for i := range orgs {
		if orgs[i].ID == orgID {
			target = &orgs[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "Organization not found", http.StatusForbidden)
		return
	}

	_ = h.sessions.SetSelectedOrg(r.Context(), sess.ID, orgID)
	http.Redirect(w, r, "/orgs/"+target.Slug, http.StatusSeeOther)
}

func (h *OrgHandler) CreateOrg(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	slug := slugify(name)

	// Create org and owner membership in a single transaction so a
	// transient failure on the membership INSERT cannot leave behind
	// an orphan org with no owner and no path for the creator to
	// manage it. (#29)
	org, err := h.db.CreateOrgWithOwnerTx(r.Context(), user.ID, name, slug, auth.OrgRoleOwner)
	if err != nil {
		log.Printf("creating org with owner: %v", err)
		http.Error(w, "Failed to create organization", http.StatusInternalServerError)
		return
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

	invitations, _ := h.db.ListOrgInvitations(r.Context(), org.ID)

	_ = h.engine.Render(w, r, "org_settings.html", render.PageData{
		Title:       org.Name + " Settings",
		User:        user,
		Org:         org,
		Orgs:        middleware.GetOrgs(r),
		Projects:    middleware.GetProjects(r),
		CurrentPath: r.URL.Path,
		Data: map[string]any{
			"Members":       members,
			"Invitations":   invitations,
			"CanManage":     canManage,
			"IsStaff":       auth.IsStaffOrAbove(user.Role),
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
	if err := h.engine.RenderPartial(w, "member_list.html", map[string]any{
		"Members":       members,
		"CanManage":     canManage,
		"CurrentUserID": user.ID,
		"OrgSlug":       org.Slug,
	}); err != nil {
		log.Printf("rendering member list partial: %v", err)
	}
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
	if target, lookupErr := h.db.GetUserByEmail(r.Context(), email); lookupErr == nil {
		if _, memErr := h.db.GetOrgMembership(r.Context(), target.ID, org.ID); memErr == nil {
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

	if err := h.engine.RenderPartial(w, "invite_result.html", map[string]any{
		"InviteURL":    inviteURL,
		"Email":        email,
		"EmailEnabled": h.mailer.IsEnabled(),
	}); err != nil {
		log.Printf("rendering invite result partial: %v", err)
	}
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

	// Protected superadmins cannot be removed by anyone
	targetUser, err := h.db.GetUserByID(r.Context(), targetID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	if h.isProtectedSuperadmin(targetUser.Email) {
		http.Error(w, "This user is protected and cannot be removed", http.StatusForbidden)
		return
	}
	if targetUser.Role == auth.RoleSuperAdmin && user.Role != auth.RoleSuperAdmin {
		http.Error(w, "Only superadmins can remove other superadmins", http.StatusForbidden)
		return
	}

	// Guarded delete serializes against concurrent owner mutations so
	// two requests on a two-owner org cannot both pass the last-owner
	// check and leave the org with zero owners. (#30)
	if err := h.db.RemoveOrgMemberGuarded(r.Context(), targetID, org.ID); err != nil {
		if errors.Is(err, models.ErrLastOwner) {
			http.Error(w, "Cannot remove the last owner", http.StatusBadRequest)
			return
		}
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

	// Protected superadmins cannot have their role changed by anyone
	targetUser, err := h.db.GetUserByID(r.Context(), targetID)
	if err != nil {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	if h.isProtectedSuperadmin(targetUser.Email) {
		http.Error(w, "This user is protected and cannot be modified", http.StatusForbidden)
		return
	}
	if targetUser.Role == auth.RoleSuperAdmin && user.Role != auth.RoleSuperAdmin {
		http.Error(w, "Only superadmins can modify other superadmins", http.StatusForbidden)
		return
	}

	// Guarded update serializes against concurrent owner mutations to
	// prevent the last-owner invariant from being bypassed by two
	// concurrent demotion requests. (#30)
	if err := h.db.UpdateOrgMemberRoleGuarded(r.Context(), targetID, org.ID, newRole); err != nil {
		if errors.Is(err, models.ErrLastOwner) {
			http.Error(w, "Cannot demote the last owner", http.StatusBadRequest)
			return
		}
		log.Printf("updating org member role: %v", err)
		http.Error(w, "Failed to update role", http.StatusInternalServerError)
		return
	}

	h.renderMemberList(w, r, org, user)
}

func (h *OrgHandler) UpdateAIMargin(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	slug := r.PathValue("orgSlug")
	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	marginStr := r.FormValue("ai_margin_percent")
	margin, err := strconv.Atoi(marginStr)
	if err != nil || margin < 0 || margin > 500 {
		http.Error(w, "Margin must be between 0 and 500", http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateOrgAIMargin(r.Context(), org.ID, margin); err != nil {
		log.Printf("updating ai margin: %v", err)
		http.Error(w, "Failed to update margin", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+slug+"/settings", http.StatusSeeOther)
}

func (h *OrgHandler) UpdateBusinessDetails(w http.ResponseWriter, r *http.Request) {
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

	if err := h.db.UpdateOrgBusinessDetails(r.Context(), org.ID,
		strings.TrimSpace(r.FormValue("business_name")),
		strings.TrimSpace(r.FormValue("vat_number")),
		strings.TrimSpace(r.FormValue("registration_number")),
		strings.TrimSpace(r.FormValue("address_street")),
		strings.TrimSpace(r.FormValue("address_extra")),
		strings.TrimSpace(r.FormValue("postal_code")),
		strings.TrimSpace(r.FormValue("city")),
		strings.TrimSpace(r.FormValue("country")),
		strings.TrimSpace(r.FormValue("contact_phones")),
		strings.TrimSpace(r.FormValue("contact_emails")),
	); err != nil {
		log.Printf("updating business details: %v", err)
		http.Error(w, "Failed to update business details", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+slug+"/settings", http.StatusSeeOther)
}

var allowedCurrencies = map[string]bool{
	"EUR": true, "USD": true, "GBP": true, "CHF": true,
	"SEK": true, "NOK": true, "DKK": true, "PLN": true,
	"CZK": true, "RON": true, "HUF": true, "BGN": true,
	"HRK": true, "JPY": true, "CAD": true, "AUD": true,
}

func (h *OrgHandler) UpdateCurrency(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if !auth.IsStaffOrAbove(user.Role) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	slug := r.PathValue("orgSlug")
	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if err != nil {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}

	code := strings.ToUpper(strings.TrimSpace(r.FormValue("currency_code")))
	if !allowedCurrencies[code] {
		http.Error(w, "Invalid currency code", http.StatusBadRequest)
		return
	}

	if err := h.db.UpdateOrgCurrency(r.Context(), org.ID, code); err != nil {
		log.Printf("updating currency: %v", err)
		http.Error(w, "Failed to update currency", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/orgs/"+slug+"/settings", http.StatusSeeOther)
}
