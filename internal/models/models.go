package models

import "time"

type User struct {
	CreatedAt          time.Time `db:"created_at"`
	UpdatedAt          time.Time `db:"updated_at"`
	Preferred2FAMethod *string   `db:"preferred_2fa_method"`
	ID                 string    `db:"id"`
	Email              string    `db:"email"`
	PasswordHash       string    `db:"password_hash"`
	Name               string    `db:"name"`
	Role               string    `db:"role"`
	TOTPSecret         []byte    `db:"totp_secret"`
	RecoveryCodes      []byte    `db:"recovery_codes"`
	TOTPVerified       bool      `db:"totp_verified"`
	MustSetup2FA       bool      `db:"must_setup_2fa"`
}

type Organization struct {
	CreatedAt          time.Time `db:"created_at"`
	UpdatedAt          time.Time `db:"updated_at"`
	AddressStreet      string    `db:"address_street"`
	AddressExtra       string    `db:"address_extra"`
	CurrencyCode       string    `db:"currency_code"`
	BusinessName       string    `db:"business_name"`
	VATNumber          string    `db:"vat_number"`
	RegistrationNumber string    `db:"registration_number"`
	ID                 string    `db:"id"`
	Name               string    `db:"name"`
	PostalCode         string    `db:"postal_code"`
	City               string    `db:"city"`
	Country            string    `db:"country"`
	ContactPhones      string    `db:"contact_phones"`
	ContactEmails      string    `db:"contact_emails"`
	Slug               string    `db:"slug"`
	AIMarginPercent    int       `db:"ai_margin_percent"`
}

type OrgMembership struct {
	CreatedAt time.Time `db:"created_at"`
	ID        string    `db:"id"`
	UserID    string    `db:"user_id"`
	OrgID     string    `db:"org_id"`
	Role      string    `db:"role"`
}

type Invitation struct {
	ExpiresAt time.Time `db:"expires_at"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	ID        string    `db:"id"`
	Email     string    `db:"email"`
	OrgID     string    `db:"org_id"`
	OrgRole   string    `db:"org_role"`
	TokenHash string    `db:"token_hash"`
	Status    string    `db:"status"`
	InvitedBy string    `db:"invited_by"`
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
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
	ID            string    `db:"id"`
	OrgID         string    `db:"org_id"`
	Name          string    `db:"name"`
	Slug          string    `db:"slug"`
	BriefMarkdown string    `db:"brief_markdown"`
	RepoURL       string    `db:"repo_url"`
}

type Ticket struct {
	CreatedAt           time.Time  `db:"created_at"`
	UpdatedAt           time.Time  `db:"updated_at"`
	AssignedTo          *string    `db:"assigned_to"`
	DateEnd             *time.Time `db:"date_end"`
	ParentID            *string    `db:"parent_id"`
	ArchivedAt          *time.Time `db:"archived_at"`
	AgentName           *string    `db:"agent_name"`
	AgentMode           *string    `db:"agent_mode"`
	DateStart           *time.Time `db:"date_start"`
	Status              string     `db:"status"`
	Priority            string     `db:"priority"`
	Type                string     `db:"type"`
	ID                  string     `db:"id"`
	CreatedBy           string     `db:"created_by"`
	DescriptionMarkdown string     `db:"description_markdown"`
	Title               string     `db:"title"`
	ProjectID           string     `db:"project_id"`
	ChildCount          int        `db:"-"`
}

type Comment struct {
	CreatedAt    time.Time `db:"created_at"`
	UserID       *string   `db:"user_id"`
	AgentName    *string   `db:"agent_name"`
	ID           string    `db:"id"`
	TicketID     string    `db:"ticket_id"`
	BodyMarkdown string    `db:"body_markdown"`
}

type TicketActivity struct {
	CreatedAt   time.Time `db:"created_at"`
	UserID      *string   `db:"user_id"`
	AgentName   *string   `db:"agent_name"`
	ID          string    `db:"id"`
	TicketID    string    `db:"ticket_id"`
	Action      string    `db:"action"`
	DetailsJSON string    `db:"details_json"`
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
	CreatedAt           time.Time  `db:"created_at"`
	LastUsedAt          *time.Time `db:"last_used_at"`
	ID                  string     `db:"id"`
	UserID              string     `db:"user_id"`
	AttestationType     string     `db:"attestation_type"`
	Name                string     `db:"name"`
	CredentialID        []byte     `db:"credential_id"`
	PublicKey           []byte     `db:"public_key"`
	AuthenticatorAAGUID []byte     `db:"authenticator_aaguid"`
	SignCount           uint32     `db:"sign_count"`
}

type AIConversation struct {
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	ProjectID *string   `db:"project_id"`
	ID        string    `db:"id"`
	UserID    string    `db:"user_id"`
	Title     string    `db:"title"`
}

type AIMessage struct {
	CreatedAt      time.Time `db:"created_at"`
	UserID         *string   `db:"user_id"`
	ID             string    `db:"id"`
	ConversationID string    `db:"conversation_id"`
	Role           string    `db:"role"`
	Content        string    `db:"content"`
	UserName       string    `db:"user_name"`
}

type ProjectCost struct {
	CreatedAt   time.Time `db:"created_at"`
	UpdatedAt   time.Time `db:"updated_at"`
	ID          string    `db:"id"`
	ProjectID   string    `db:"project_id"`
	Month       string    `db:"month"`
	Category    string    `db:"category"`
	Name        string    `db:"name"`
	AmountCents int64     `db:"amount_cents"`
}

type AIUsageEntry struct {
	CreatedAt    time.Time `db:"created_at"`
	ProjectID    *string   `db:"project_id"`
	ID           string    `db:"id"`
	OrgID        string    `db:"org_id"`
	UserID       string    `db:"user_id"`
	Model        string    `db:"model"`
	Label        string    `db:"label"`
	InputTokens  int       `db:"input_tokens"`
	OutputTokens int       `db:"output_tokens"`
	CostCents    int64     `db:"cost_cents"`
}

type AIUsageSummary struct {
	Model        string
	Label        string
	InputTokens  int64
	OutputTokens int64
	TotalCents   int64
	EntryCount   int
}

type TicketAttachment struct {
	CreatedAt   time.Time `db:"created_at"`
	ID          string    `db:"id"`
	TicketID    string    `db:"ticket_id"`
	FileName    string    `db:"file_name"`
	FilePath    string    `db:"file_path"`
	ContentType string    `db:"content_type"`
	UploadedBy  string    `db:"uploaded_by"`
	SizeBytes   int64     `db:"size_bytes"`
}

type Reaction struct {
	CreatedAt  time.Time `db:"created_at"`
	ID         string    `db:"id"`
	TargetType string    `db:"target_type"`
	TargetID   string    `db:"target_id"`
	UserID     string    `db:"user_id"`
	Emoji      string    `db:"emoji"`
}

type ReactionGroup struct {
	Emoji       string
	Count       int
	UserReacted bool
}

type PlatformSettings struct {
	CreatedAt          time.Time `db:"created_at"`
	UpdatedAt          time.Time `db:"updated_at"`
	BusinessName       string    `db:"business_name"`
	VATNumber          string    `db:"vat_number"`
	RegistrationNumber string    `db:"registration_number"`
	AddressStreet      string    `db:"address_street"`
	AddressExtra       string    `db:"address_extra"`
	PostalCode         string    `db:"postal_code"`
	City               string    `db:"city"`
	Country            string    `db:"country"`
	ContactPhones      string    `db:"contact_phones"`
	ContactEmails      string    `db:"contact_emails"`
}
