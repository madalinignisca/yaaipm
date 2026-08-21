package handlers

import (
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

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

	// Access gate. MUST come before any org data is loaded: without it any
	// authenticated user could read any org's settings by guessing a slug
	// (slugs are slugify(name), so they are guessable), exposing the member
	// roster across tenants.
	//
	// Uses the shared authorizeOrgAccess helper rather than an inline
	// membership check so this page inherits the documented convention:
	// "not a member" and "no such org" both surface as 404, so probing
	// cannot distinguish an org that exists from one that does not.
	if authErr := authorizeOrgAccess(r.Context(), h.db, user, org.ID); authErr != nil {
		if errors.Is(authErr, errCrossTenant) {
			h.engine.RenderError(w, http.StatusNotFound, "Organization not found")
			return
		}
		// Real infrastructure failure — never report a DB fault as an
		// authorization decision.
		log.Printf("org settings authz for user %s org %s: %v", user.ID, org.ID, authErr)
		h.engine.RenderError(w, http.StatusInternalServerError, "Failed to load organization")
		return
	}

	// Separate question from "may they be here": may they EDIT. The bug
	// this fix closes was answering only this one and letting the page
	// render regardless.
	canManage := auth.IsStaffOrAbove(user.Role)
	if !canManage {
		if m, memErr := h.db.GetOrgMembership(r.Context(), user.ID, org.ID); memErr == nil {
			canManage = auth.CanManageOrg(m.Role)
		}
	}

	members, err := h.db.ListOrgMembers(r.Context(), org.ID)
	if err != nil {
		h.engine.RenderError(w, http.StatusInternalServerError, "Failed to load members")
		return
	}

	invitations, _ := h.db.ListOrgInvitations(r.Context(), org.ID)

	// Capture `now` ONCE and derive both the aggregate range and the
	// displayed month label from it, so the two can never disagree
	// across a midnight boundary between computing one and the other.
	now := time.Now()
	budgetFields := h.buildBudgetSettingsFields(r, org, now)

	_ = h.engine.Render(w, r, "org_settings.html", render.PageData{
		Title:       org.Name + " Settings",
		User:        user,
		Org:         org,
		Orgs:        middleware.GetOrgs(r),
		Projects:    middleware.GetProjects(r),
		CurrentPath: r.URL.Path,
		Data: mergeMap(map[string]any{
			"Members":       members,
			"Invitations":   invitations,
			"CanManage":     canManage,
			"IsStaff":       auth.IsStaffOrAbove(user.Role),
			"CurrentUserID": user.ID,
			"OrgSlug":       org.Slug,
		}, budgetFields),
	})
}

// mergeMap combines two string-keyed maps into one new map (b's keys win
// on conflict, though none are expected to overlap here). Kept tiny and
// local rather than pulling in a third-party helper for a single call
// site; stdlib maps.Copy does the actual copying.
func mergeMap(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	maps.Copy(out, a)
	maps.Copy(out, b)
	return out
}

// budgetSettingsFields carries every Go-computed value the budget card
// template needs. All money is pre-formatted as strings here so the
// template never does float arithmetic or a new FuncMap entry (spec §6
// forbids float on a money control).
type budgetSettingsFields struct {
	CanViewBudget          bool
	BudgetIsUnlimited      bool
	BudgetCapInput         string // value= for the <input>, empty when unlimited
	BudgetCapDisplay       string // human "Unlimited" or "$X.XX"
	BudgetSpendDisplay     string // "$X.XX" or "" when unavailable
	BudgetSpendUnavailable bool
	BudgetMonth            string // e.g. "August 2026"
}

// buildBudgetSettingsFields computes the budget card's display data.
// Any org member (or staff) who reached OrgSettings at all may VIEW this
// — the costs page is already member-visible and already shows debate
// spend inside infraTotal, so this exposes no new granularity (spec §6).
//
// An aggregate failure here is NOT fatal to the page (unlike the
// enforcement path in checkOrgBudget, which fails closed) — showing a
// dash for spend is harmless; treating unknown spend as zero at the
// enforcement gate is not. These two paths deliberately diverge.
func (h *OrgHandler) buildBudgetSettingsFields(r *http.Request, org *models.Organization, now time.Time) map[string]any {
	from, to := models.CurrentUTCMonthRange(now)

	fields := budgetSettingsFields{
		CanViewBudget: true,
		BudgetMonth:   now.UTC().Format("January 2006"),
	}

	if org.MonthlyBudgetCents == nil {
		fields.BudgetIsUnlimited = true
		fields.BudgetCapDisplay = "Unlimited"
	} else {
		fields.BudgetCapInput = formatUSDCents(*org.MonthlyBudgetCents)
		fields.BudgetCapDisplay = "$" + formatUSDCents(*org.MonthlyBudgetCents) + " USD"
	}

	spendMicros, err := h.db.SumOrgDebateSpendMicros(r.Context(), org.ID, from, to)
	if err != nil {
		log.Printf("org settings: summing debate spend for org %s: %v", org.ID, err)
		fields.BudgetSpendUnavailable = true
	} else {
		// micros -> cents, half-up: (micros+5000)/10000. microsPerCent is
		// 10_000, so half a cent is 5_000 micros.
		// Quotient/remainder rather than (micros+5000)/10000: the additive
		// form overflows int64 for an aggregate near MaxInt64 and would
		// render a negative amount. Unreachable at real spend, but a
		// display path should not be the thing that wraps.
		spendCents := spendMicros / 10000
		if spendMicros%10000 >= 5000 {
			spendCents++
		}
		fields.BudgetSpendDisplay = "$" + formatUSDCents(spendCents) + " USD"
	}

	return map[string]any{
		"CanViewBudget":          fields.CanViewBudget,
		"BudgetIsUnlimited":      fields.BudgetIsUnlimited,
		"BudgetCapInput":         fields.BudgetCapInput,
		"BudgetCapDisplay":       fields.BudgetCapDisplay,
		"BudgetSpendDisplay":     fields.BudgetSpendDisplay,
		"BudgetSpendUnavailable": fields.BudgetSpendUnavailable,
		"BudgetMonth":            fields.BudgetMonth,
	}
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
		// Handle the race where the pre-check saw no pending invite
		// but a concurrent request created one before we INSERTed.
		// The partial-unique-index violation is idempotent behavior,
		// not an infrastructure failure — 409, not 500. (#32)
		if errors.Is(err, models.ErrDuplicatePendingInvitation) {
			http.Error(w, "An invitation is already pending for this email", http.StatusConflict)
			return
		}
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
	switch err := h.db.RemoveOrgMemberGuarded(r.Context(), targetID, org.ID); {
	case err == nil:
		// fallthrough to render
	case errors.Is(err, models.ErrMemberNotFound):
		http.Error(w, "Member not found", http.StatusNotFound)
		return
	case errors.Is(err, models.ErrLastOwner):
		http.Error(w, "Cannot remove the last owner", http.StatusBadRequest)
		return
	default:
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
	switch err := h.db.UpdateOrgMemberRoleGuarded(r.Context(), targetID, org.ID, newRole); {
	case err == nil:
		// fallthrough to render
	case errors.Is(err, models.ErrMemberNotFound):
		http.Error(w, "Member not found", http.StatusNotFound)
		return
	case errors.Is(err, models.ErrLastOwner):
		http.Error(w, "Cannot demote the last owner", http.StatusBadRequest)
		return
	default:
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

// UpdateMonthlyBudget sets or clears an org's monthly AI debate budget
// cap (spec §6, plan step 5). NOT staff-only, unlike UpdateAIMargin: the
// budget is the client's own spend control, so org owner/admin may set
// it for their own org. Staff/superadmin may set it for ANY org,
// consistent with the margin precedent.
//
// The org is derived from the route slug ONLY — no org_id form field is
// read, so there is nothing for a cross-org request to substitute.
func (h *OrgHandler) UpdateMonthlyBudget(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	slug := r.PathValue("orgSlug")

	// Distinct 404 (unknown slug) vs 500 (real DB failure) — UpdateAIMargin
	// collapses these into one branch; that is a known gap, not the
	// pattern to copy (plan step 5).
	org, err := h.db.GetOrgBySlug(r.Context(), slug)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	}
	if err != nil {
		log.Printf("looking up org %q for budget update: %v", slug, err)
		http.Error(w, "Failed to load organization", http.StatusInternalServerError)
		return
	}

	if !h.canManageOrgMembers(r, user, org.ID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	raw := r.FormValue("monthly_budget_usd")
	var capCents *int64
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		cents, parseErr := parseUSDCents(trimmed)
		switch {
		case errors.Is(parseErr, errBudgetTooManyDecimals):
			http.Error(w, errBudgetTooManyDecimals.Error(), http.StatusBadRequest)
			return
		case errors.Is(parseErr, errBudgetOutOfRange):
			http.Error(w, errBudgetOutOfRange.Error(), http.StatusBadRequest)
			return
		case parseErr != nil:
			http.Error(w, errBudgetNotANumber.Error(), http.StatusBadRequest)
			return
		}
		capCents = &cents
	}
	// Empty field (raw == "" after trim) leaves capCents nil — "leave
	// blank for unlimited" per the helper text in the settings card.

	switch err := h.db.UpdateOrgMonthlyBudget(r.Context(), org.ID, user.ID, capCents); {
	case err == nil:
		capDesc := "unlimited"
		if capCents != nil {
			capDesc = strconv.FormatInt(*capCents, 10) + " cents"
		}
		log.Printf("org %s monthly budget updated by user %s: %s", org.ID, user.ID, capDesc)
	case errors.Is(err, models.ErrOrgNotFound):
		// The org existed at the GetOrgBySlug above but was deleted
		// before this write reached it — genuinely rare, but distinct
		// from the parse-validation 400s above and from a generic 500.
		http.Error(w, "Organization not found", http.StatusNotFound)
		return
	default:
		log.Printf("updating monthly budget for org %s: %v", org.ID, err)
		http.Error(w, "Failed to update budget", http.StatusInternalServerError)
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

// Distinct sentinels so the set-budget handler can return distinct 400s
// per failure mode, matching this repo's convention of never collapsing
// DB-error / not-found / validation-failure into a single branch.
var (
	errBudgetNotANumber      = errors.New("that doesn't look like a dollar amount — use a plain number like 25 or 25.50")
	errBudgetTooManyDecimals = errors.New("use at most two decimal places")
	errBudgetOutOfRange      = errors.New("amount is too large — the maximum is $1,000,000")
)

// maxBudgetDollars mirrors models.maxBudgetCents (100_000_000 cents =
// USD 1,000,000), expressed as whole dollars. Checked BEFORE multiplying
// by 100 (spec §6): a caller supplying a huge but syntactically valid
// dollar figure must be rejected by comparison, not by letting
// dollars*100 run first and hoping it doesn't overflow.
const maxBudgetDollars = 1_000_000

// maxBudgetCentsInput is the cents-side twin of maxBudgetDollars, used
// for the final range check after the fractional part is folded in
// (e.g. "1000000.01" passes the whole-dollar check but must still be
// rejected). Duplicated from models.maxBudgetCents rather than
// exported/shared — it is a parsing-layer constant, not a persistence
// one, and the two are independently pinned by tests on both sides.
const maxBudgetCentsInput = maxBudgetDollars * 100

// isAllASCIIDigits reports whether every byte in s is '0'-'9'. Used to
// validate the lexical grammar BEFORE handing substrings to ParseInt:
// ParseInt alone would accept a leading '+' (letting "+25" through a
// grammar that declares it invalid) and doesn't reject non-digit bytes
// like ',' or 'e' as a unit — it stops at the first invalid rune, which
// would silently truncate rather than reject.
func isAllASCIIDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseUSDCents converts a decimal USD string to integer cents without
// any floating point (spec §6 forbids float arithmetic on a money
// control — ParseFloat+Round, as costs.go's parseToCents uses, silently
// accepts scientific notation ("1e3") and NaN/Inf, and rounds instead of
// rejecting a third decimal place). Accepts an optional leading '$' and
// surrounding whitespace; otherwise the trimmed string must match
// ^\d+(\.\d{1,2})?$.
func parseUSDCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errBudgetNotANumber
	}

	dollarsStr, fracStr, hasDot := strings.Cut(s, ".")
	if dollarsStr == "" || !isAllASCIIDigits(dollarsStr) {
		return 0, errBudgetNotANumber
	}
	if hasDot {
		if fracStr == "" || !isAllASCIIDigits(fracStr) {
			return 0, errBudgetNotANumber
		}
		if len(fracStr) > 2 {
			return 0, errBudgetTooManyDecimals
		}
	}

	dollars, err := strconv.ParseInt(dollarsStr, 10, 64)
	if err != nil {
		// Only reachable for a dollar string ParseInt itself can't hold
		// (e.g. more digits than fit in int64) — isAllASCIIDigits already
		// rejected every other malformed shape.
		return 0, errBudgetNotANumber
	}
	if dollars > maxBudgetDollars {
		return 0, errBudgetOutOfRange
	}

	// Right-pad the fraction to exactly 2 digits ("5" -> "50") so e.g.
	// "25.5" and "25.50" both parse to 2550 cents.
	frac := fracStr
	for len(frac) < 2 {
		frac += "0"
	}
	var fracCents int64
	if frac != "" {
		fracCents, err = strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return 0, errBudgetNotANumber
		}
	}

	// dollars <= maxBudgetDollars here, so dollars*100 cannot overflow;
	// fracCents is at most 99. The final bound check below still catches
	// e.g. "1000000.01", which passed the whole-dollar check above but
	// pushes the total over the cap once the cents are folded in.
	cents := dollars*100 + fracCents
	if cents > maxBudgetCentsInput {
		return 0, errBudgetOutOfRange
	}
	return cents, nil
}

// formatUSDCents renders integer cents as a plain decimal string for
// display/pre-fill ("2550" -> "25.50"). Callers should never pass a
// negative value (the setter and DB CHECK both reject them before
// persistence), but %02d of a negative remainder renders "0.-5", not
// "-0.05" — this guard exists to catch that class of bug rather than
// display a malformed figure.
func formatUSDCents(cents int64) string {
	// Negatives should never reach here — spend and caps are both
	// non-negative by construction. Handle them anyway because the naive
	// form renders -5 cents as "0.-5": %02d of a negative remainder keeps
	// the sign. Formatting the magnitude and prefixing the sign is the
	// only version that cannot emit garbage.
	sign := ""
	abs := cents
	if cents < 0 {
		sign = "-"
		abs = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, abs/100, abs%100)
}
