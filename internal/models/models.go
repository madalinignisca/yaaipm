package models

import "time"

type User struct {
	ID                 string     `db:"id"`
	Email              string     `db:"email"`
	PasswordHash       string     `db:"password_hash"`
	Name               string     `db:"name"`
	Role               string     `db:"role"`
	TOTPSecret         []byte     `db:"totp_secret"`
	TOTPVerified       bool       `db:"totp_verified"`
	RecoveryCodes      []byte     `db:"recovery_codes"`
	MustSetup2FA       bool       `db:"must_setup_2fa"`
	Preferred2FAMethod *string    `db:"preferred_2fa_method"`
	CreatedAt          time.Time  `db:"created_at"`
	UpdatedAt          time.Time  `db:"updated_at"`
}

type Organization struct {
	ID              string    `db:"id"`
	Name            string    `db:"name"`
	Slug            string    `db:"slug"`
	AIMarginPercent int       `db:"ai_margin_percent"`
	CreatedAt       time.Time `db:"created_at"`
	UpdatedAt       time.Time `db:"updated_at"`
}

type OrgMembership struct {
	ID        string    `db:"id"`
	UserID    string    `db:"user_id"`
	OrgID     string    `db:"org_id"`
	Role      string    `db:"role"`
	CreatedAt time.Time `db:"created_at"`
}

type Invitation struct {
	ID        string    `db:"id"`
	Email     string    `db:"email"`
	OrgID     string    `db:"org_id"`
	OrgRole   string    `db:"org_role"`
	TokenHash string    `db:"token_hash"`
	Status    string    `db:"status"`
	InvitedBy string    `db:"invited_by"`
	ExpiresAt time.Time `db:"expires_at"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type InvitationWithOrg struct {
	Invitation   Invitation
	Organization Organization
	InviterName  string
}

type InvitationWithInviter struct {
	Invitation  Invitation
	InviterName string
}

type Project struct {
	ID            string    `db:"id"`
	OrgID         string    `db:"org_id"`
	Name          string    `db:"name"`
	Slug          string    `db:"slug"`
	BriefMarkdown string    `db:"brief_markdown"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

type Ticket struct {
	ID                  string     `db:"id"`
	ProjectID           string     `db:"project_id"`
	ParentID            *string    `db:"parent_id"`
	Type                string     `db:"type"`
	Title               string     `db:"title"`
	DescriptionMarkdown string     `db:"description_markdown"`
	Status              string     `db:"status"`
	Priority            string     `db:"priority"`
	DateStart           *time.Time `db:"date_start"`
	DateEnd             *time.Time `db:"date_end"`
	AgentMode           *string    `db:"agent_mode"`
	AgentName           *string    `db:"agent_name"`
	AssignedTo          *string    `db:"assigned_to"`
	CreatedBy           string     `db:"created_by"`
	ArchivedAt          *time.Time `db:"archived_at"`
	CreatedAt           time.Time  `db:"created_at"`
	UpdatedAt           time.Time  `db:"updated_at"`
}

type Comment struct {
	ID           string    `db:"id"`
	TicketID     string    `db:"ticket_id"`
	UserID       *string   `db:"user_id"`
	AgentName    *string   `db:"agent_name"`
	BodyMarkdown string    `db:"body_markdown"`
	CreatedAt    time.Time `db:"created_at"`
}

type TicketActivity struct {
	ID          string    `db:"id"`
	TicketID    string    `db:"ticket_id"`
	UserID      *string   `db:"user_id"`
	AgentName   *string   `db:"agent_name"`
	Action      string    `db:"action"`
	DetailsJSON string    `db:"details_json"`
	CreatedAt   time.Time `db:"created_at"`
}

type BriefRevision struct {
	ID            string    `db:"id"`
	ProjectID     string    `db:"project_id"`
	UserID        string    `db:"user_id"`
	Action        string    `db:"action"`
	PreviousBrief string    `db:"previous_brief"`
	CreatedAt     time.Time `db:"created_at"`
	// Joined fields (not in table)
	UserName string `db:"user_name"`
}

type WebAuthnCredential struct {
	ID                string     `db:"id"`
	UserID            string     `db:"user_id"`
	CredentialID      []byte     `db:"credential_id"`
	PublicKey         []byte     `db:"public_key"`
	AttestationType   string     `db:"attestation_type"`
	AuthenticatorAAGUID []byte   `db:"authenticator_aaguid"`
	SignCount         uint32     `db:"sign_count"`
	Name              string     `db:"name"`
	LastUsedAt        *time.Time `db:"last_used_at"`
	CreatedAt         time.Time  `db:"created_at"`
}

type AIConversation struct {
	ID        string    `db:"id"`
	UserID    string    `db:"user_id"`
	ProjectID *string   `db:"project_id"`
	Title     string    `db:"title"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

type AIMessage struct {
	ID             string    `db:"id"`
	ConversationID string    `db:"conversation_id"`
	Role           string    `db:"role"`
	Content        string    `db:"content"`
	UserID         *string   `db:"user_id"`
	UserName       string    `db:"user_name"`
	CreatedAt      time.Time `db:"created_at"`
}

type ProjectCost struct {
	ID          string    `db:"id"`
	ProjectID   string    `db:"project_id"`
	Month       string    `db:"month"`
	Category    string    `db:"category"`
	Name        string    `db:"name"`
	AmountCents int64     `db:"amount_cents"`
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
}

type AIUsageEntry struct {
	ID           string    `db:"id"`
	OrgID        string    `db:"org_id"`
	UserID       string    `db:"user_id"`
	Model        string    `db:"model"`
	Label        string    `db:"label"`
	ProjectID    *string   `db:"project_id"`
	InputTokens  int       `db:"input_tokens"`
	OutputTokens int       `db:"output_tokens"`
	CostCents    int64     `db:"cost_cents"`
	CreatedAt    time.Time `db:"created_at"`
}

type AIModelPricing struct {
	ID                          string    `db:"id"`
	ModelName                   string    `db:"model_name"`
	InputPricePerMillionCents   int64     `db:"input_price_per_million_cents"`
	OutputPricePerMillionCents  int64     `db:"output_price_per_million_cents"`
	EffectiveFrom               time.Time `db:"effective_from"`
	CreatedAt                   time.Time `db:"created_at"`
}

type AIUsageSummary struct {
	Model        string
	Label        string
	InputTokens  int64
	OutputTokens int64
	TotalCents   int64
	EntryCount   int
}

type Reaction struct {
	ID         string    `db:"id"`
	TargetType string    `db:"target_type"`
	TargetID   string    `db:"target_id"`
	UserID     string    `db:"user_id"`
	Emoji      string    `db:"emoji"`
	CreatedAt  time.Time `db:"created_at"`
}

type ReactionGroup struct {
	Emoji       string
	Count       int
	UserReacted bool
}
