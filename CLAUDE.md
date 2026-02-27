# CLAUDE.md — ForgeDesk

## Project Overview

ForgeDesk is a self-hosted service desk and project management platform designed for software consulting workflows. It bridges non-technical clients with AI-powered development agents. Clients manage projects through a simple interface (briefs, features, bugs, Gantt charts), while an internal orchestrator dispatches work to coding agents (Claude Code, Gemini, Codex, Mistral) running on a VM with full access to a Kubernetes cluster.

### Core Value Proposition

Clients interact with a clean, simple PM tool. Behind the scenes, an orchestrator picks up approved tickets, delegates them to AI agents, deploys results to per-project test environments, and awaits human approval before merging to production.

---

## Tech Stack

### Backend
- **Language:** Go (latest stable)
- **HTTP Router:** Use `net/http` with Go 1.22+ routing patterns (method + path), or `chi` if more middleware is needed
- **Database:** PostgreSQL (available on the infrastructure VM)
- **Migrations:** Use `golang-migrate/migrate` with SQL migration files in `migrations/`
- **Auth:** Session-based with secure cookies. Argon2id for password hashing (not bcrypt — resistant to GPU/ASIC attacks). TOTP-based 2FA mandatory for all users. Role-based access control (RBAC)
- **2FA:** `pquerna/otp` for TOTP generation and validation. `go-webauthn/webauthn` for FIDO2/WebAuthn passkeys and security keys. QR code rendered server-side with `skip2/go-qrcode`
- **Background Jobs:** Internal goroutine-based worker pool for the orchestrator daemon. No external queue needed initially — poll the DB

### Frontend
- **Rendering:** Server-side rendered HTML templates using Go `html/template`
- **Interactivity:** HTMX for dynamic page updates without full reloads
- **Client-side state:** Alpine.js for lightweight UI behavior (dropdowns, modals, tabs, toggles)
- **Charts:** D3.js for Gantt chart and any future data visualizations
- **CSS:** Tailwind CSS via standalone CLI (no Node.js build step required)
- **Icons:** Lucide icons (SVG, inline)

### Infrastructure & Deployment
- **Container Runtime:** Podman (not Docker)
- **Orchestration:** Kubernetes cluster accessible from the VM
- **Database:** Managed PostgreSQL. Root credentials stored in `.secrets` file (never committed to git, listed in `.gitignore`)
- **TLS Certificate:** `smart.madalin.me.crt` (certificate) and `smart.madalin.me.key` (private key)
- **Ingress:** Configured per-project for test environments, using the `smart.madalin.me` wildcard cert
- **CI/CD:** Git-based. Agent work happens on branches, PRs for review, merge triggers production deploy
- **OS:** AlmaLinux (RHEL-family)

---

## Architecture

### Directory Structure

```
forgedesk/
├── CLAUDE.md
├── .gitignore                # Must include: .secrets, *.key, *.crt
├── .secrets                  # Postgres creds, API keys, encryption keys (NEVER committed)
├── smart.madalin.me.crt      # TLS certificate (NEVER committed)
├── smart.madalin.me.key      # TLS private key (NEVER committed)
├── go.mod
├── go.sum
├── cmd/
│   ├── server/          # Main web application
│   │   └── main.go
│   └── orchestrator/    # Agent orchestrator daemon
│       └── main.go
├── internal/
│   ├── auth/            # Authentication, sessions, RBAC
│   │   ├── session.go       # Session creation, validation, invalidation
│   │   ├── password.go      # Argon2id hashing and verification
│   │   ├── totp.go          # TOTP setup, verification, QR generation
│   │   ├── webauthn.go      # WebAuthn registration, assertion, credential management
│   │   ├── recovery.go      # Recovery code generation, hashing, validation
│   │   └── rbac.go          # Role checks, permission helpers
│   ├── crypto/          # Encryption utilities
│   │   └── aes.go           # AES-256-GCM encrypt/decrypt for TOTP secrets at rest
│   ├── models/          # Database models and queries
│   ├── handlers/        # HTTP handlers grouped by domain
│   │   ├── auth.go          # Login, logout, 2FA setup, 2FA verify, password change
│   │   ├── dashboard.go
│   │   ├── orgs.go
│   │   ├── projects.go
│   │   ├── tickets.go
│   │   ├── comments.go
│   │   ├── users.go         # User management, 2FA reset actions
│   │   └── admin.go
│   ├── orchestrator/    # Orchestrator logic
│   │   ├── dispatcher.go    # Job dispatch loop
│   │   ├── agents.go        # Agent interface + implementations
│   │   ├── context.go       # Context assembly for agents
│   │   ├── deployer.go      # K8s test environment provisioning
│   │   └── state.go         # Ticket state machine
│   ├── middleware/       # Auth, logging, RBAC middleware
│   ├── render/           # Template rendering helpers
│   └── config/           # Configuration loading
├── migrations/           # SQL migration files (sequential numbered)
├── templates/            # Go HTML templates
│   ├── layouts/
│   │   └── base.html
│   ├── components/       # Reusable HTMX partials
│   │   ├── ticket_card.html
│   │   ├── comment.html
│   │   ├── gantt.html
│   │   └── modal.html
│   ├── pages/
│   │   ├── dashboard.html
│   │   ├── project_brief.html
│   │   ├── project_features.html
│   │   ├── project_bugs.html
│   │   ├── project_gantt.html
│   │   ├── org_settings.html
│   │   └── admin.html
│   └── auth/
│       ├── login.html
│       ├── register.html
│       ├── setup_2fa.html        # Method chooser: TOTP or WebAuthn (dead-end page, no nav)
│       ├── setup_2fa_totp.html   # QR code display, TOTP verification form (HTMX partial)
│       ├── setup_2fa_webauthn.html # WebAuthn registration prompt (HTMX partial)
│       ├── verify_2fa.html       # Login verification: shows preferred method, fallback links
│       └── recovery_codes.html   # One-time display after 2FA setup
├── static/
│   ├── css/
│   │   └── app.css       # Tailwind output
│   ├── js/
│   │   ├── htmx.min.js
│   │   ├── alpine.min.js
│   │   ├── gantt.js       # D3-based Gantt chart module
│   │   └── webauthn.js    # WebAuthn browser API helpers (credentials.create, credentials.get)
│   └── img/
├── scripts/
│   ├── tailwind-build.sh
│   └── seed.sh
└── deploy/
    ├── Containerfile      # Podman container build (NOT Dockerfile)
    ├── k8s/               # K8s manifests for ForgeDesk itself
    │   ├── namespace.yaml
    │   ├── deployment.yaml
    │   ├── service.yaml
    │   ├── ingress.yaml
    │   └── secrets.yaml   # Generated from .secrets, never committed
    └── templates/         # K8s manifest templates for client test envs
```

### Data Model (Core Entities)

```
Organization
├── id, name, slug, created_at, updated_at
├── has many: Projects, Users (via OrgMembership)
│
├── OrgMembership (user_id, org_id, role: owner|admin|member)
│
└── Project
    ├── id, org_id, name, slug, brief_markdown, created_at, updated_at
    ├── has many: Tickets
    │
    └── Ticket
        ├── id, project_id, parent_id (nullable, for subtasks)
        ├── type: epic|task|subtask|bug
        ├── title, description_markdown
        ├── status: backlog|ready|planning|plan_review|implementing|testing|review|done|cancelled
        ├── priority: low|medium|high|critical
        ├── date_start, date_end (nullable, used by Gantt)
        ├── agent_mode: null|plan|implement (set by internal staff)
        ├── agent_name: null|claude|gemini|codex|mistral
        ├── assigned_to (nullable, user_id — for human assignment)
        ├── created_by (user_id)
        ├── created_at, updated_at
        │
        ├── has many: Comments
        │   └── Comment
        │       ├── id, ticket_id, user_id (nullable if agent), agent_name (nullable)
        │       ├── body_markdown
        │       └── created_at
        │
        └── has many: TicketActivity (audit log)
            ├── id, ticket_id, user_id, agent_name
            ├── action: status_change|comment|assignment|deploy|merge
            ├── details_json
            └── created_at

User
├── id, email, password_hash (argon2id), name, role: superadmin|staff|client
├── totp_secret (encrypted at rest, nullable — null means TOTP not configured)
├── totp_verified (bool, false until first successful TOTP validation)
├── recovery_codes (encrypted JSON array, 10 single-use codes)
├── must_setup_2fa (bool, default true — set false only after verified 2FA setup)
├── preferred_2fa_method: totp|webauthn (set during setup, used to route login flow)
├── created_at, updated_at
├── has many: OrgMemberships
└── has many: WebAuthnCredentials

WebAuthnCredential
├── id, user_id
├── credential_id (bytes, unique — the key identifier from the authenticator)
├── public_key (bytes — COSE-encoded public key)
├── attestation_type (string)
├── authenticator_aaguid (bytes — identifies the make/model of the key)
├── sign_count (uint32 — incremented on each use, detect cloned keys)
├── name (string — user-friendly label, e.g. "YubiKey 5C", "MacBook Touch ID")
├── last_used_at (nullable timestamp)
├── created_at
└── belongs to: User
```

### User Roles & Access

| Role        | Scope        | Capabilities |
|-------------|--------------|--------------|
| superadmin  | Global       | Everything. Create orgs, manage staff, configure agents, trigger merges/deploys |
| staff       | Global       | View all orgs/projects. Set agent_mode on tickets. Approve plans. Trigger merge & production deploy |
| client      | Per-org      | Manage own org's users. Create/edit tickets in assigned projects. View briefs, features, bugs, Gantt. Comment on tickets. Cannot see agent internals or trigger deploys |

**Key rule:** Clients should never see orchestrator internals. Agent comments appear as "ForgeDesk Bot" or similar. The agent_mode and agent_name fields are only visible/editable by staff and superadmin.

### Authentication & 2FA

#### Password Policy
- Argon2id with parameters: memory=64MB, iterations=3, parallelism=4, keyLength=32
- Minimum 12 characters. No complexity rules — length is security
- Passwords checked against HaveIBeenPwned API (k-anonymity model, only send first 5 chars of SHA-1 hash) on registration and password change

#### 2FA Enforcement (Non-Negotiable)

Every user must complete 2FA setup on first login. There is no way to skip, defer, or dismiss this. The system is designed so that no authenticated route is accessible without a verified 2FA setup. Users choose their method: TOTP (authenticator app) or WebAuthn (hardware security key / passkey). They can register multiple methods and multiple credentials per method.

**Login flow:**

```
POST /login (email + password)
  ├── credentials invalid → error, stay on login
  ├── credentials valid, must_setup_2fa == true (first login / 2FA reset)
  │   └── redirect to /setup-2fa (isolated route, no sidebar, no navigation)
  │       ├── User chooses method: "Authenticator App" or "Security Key / Passkey"
  │       │
  │       ├── TOTP path:
  │       │   ├── Generate TOTP secret, display QR code + manual key
  │       │   ├── User scans with authenticator app
  │       │   ├── User enters 6-digit code to verify
  │       │   └── On success: set totp_verified = true, preferred_2fa_method = totp
  │       │
  │       ├── WebAuthn path:
  │       │   ├── Call navigator.credentials.create() — browser prompts for key/biometric
  │       │   ├── Server validates attestation, stores credential in WebAuthnCredentials table
  │       │   ├── User names the credential (e.g. "YubiKey Office", "MacBook")
  │       │   └── On success: set preferred_2fa_method = webauthn
  │       │
  │       └── After either method succeeds:
  │           ├── set must_setup_2fa = false
  │           ├── Generate and show 10 recovery codes (one-time display)
  │           └── redirect to dashboard
  │
  └── credentials valid, must_setup_2fa == false (returning user)
      └── redirect to /verify-2fa
          ├── Page detects preferred_2fa_method and shows appropriate UI:
          │
          ├── WebAuthn preferred (auto-triggered):
          │   ├── Call navigator.credentials.get() — browser prompts key/biometric
          │   ├── Server validates assertion, checks sign_count (detect cloned keys)
          │   ├── Update sign_count and last_used_at
          │   ├── On success: create full session, redirect to dashboard
          │   └── On failure: show "Try another method" link → falls back to TOTP or recovery
          │
          ├── TOTP preferred:
          │   ├── User enters 6-digit TOTP code
          │   ├── On success: create full session, redirect to dashboard
          │   └── On failure: retry (rate limited, 5 attempts then 15min lockout)
          │
          └── Fallback (always available):
              ├── "Use authenticator app instead" (if TOTP configured)
              ├── "Use security key instead" (if WebAuthn credentials exist)
              └── "Use a recovery code" (always available as last resort)
```

**WebAuthn server configuration:**

```go
webauthnConfig := &webauthn.Config{
    RPDisplayName: "ForgeDesk",                    // Shown in browser prompts
    RPID:          "forgedesk.example.com",        // Must match the domain exactly
    RPOrigins:     []string{"https://forgedesk.example.com"},
    AttestationPreference: protocol.PreferDirectAttestation,
    AuthenticatorSelection: protocol.AuthenticatorSelection{
        // Allow both platform (Touch ID, Windows Hello) and cross-platform (YubiKey)
        AuthenticatorAttachment: "",                // No preference — allow all
        UserVerification:        protocol.VerificationPreferred,
        ResidentKey:             protocol.ResidentKeyPreferred, // Support passkeys
    },
}
```

**Multiple credentials support:**
- Users can register multiple WebAuthn credentials (e.g. YubiKey on keychain + laptop Touch ID + backup key in safe)
- Users can register TOTP AND WebAuthn simultaneously — both work for login
- Managed from account settings: `/account/security` page lists all registered methods
- Each WebAuthn credential shows: name, last used date, registration date, remove button
- Removing the last 2FA method is blocked — at least one must remain active at all times

**Middleware enforcement:**

```go
// AuthMiddleware runs on ALL routes except: /login, /setup-2fa, /verify-2fa, /static/*
func AuthMiddleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        session := getSession(r)

        // Not logged in at all
        if session.UserID == "" {
            http.Redirect(w, r, "/login", http.StatusSeeOther)
            return
        }

        // Logged in but 2FA not yet set up
        if session.MustSetup2FA {
            http.Redirect(w, r, "/setup-2fa", http.StatusSeeOther)
            return
        }

        // Logged in but 2FA not verified this session
        if !session.TwoFactorVerified {
            http.Redirect(w, r, "/verify-2fa", http.StatusSeeOther)
            return
        }

        next.ServeHTTP(w, r)
    })
}
```

**Key design rules:**
- The `/setup-2fa` page is a dead-end. No nav, no sidebar, no links to other pages. The only way out is completing setup
- Session cookie has two phases: `authenticated` (password OK) and `2fa_verified` (full access). Only `2fa_verified` sessions pass the middleware
- TOTP window: allow ±1 time step (30 seconds tolerance) to handle clock drift
- WebAuthn challenge: generated per-attempt, stored in session, expires after 60 seconds
- Recovery codes: 10 codes, alphanumeric, 8 characters each. Each code is single-use. Stored as Argon2id hashes (same as passwords). When a recovery code is used, mark it consumed in the DB
- Recovery codes are shown exactly once — on 2FA setup completion. The user must copy/save them. There is no "show recovery codes again" feature
- Sign count verification: if a WebAuthn assertion has a sign_count less than or equal to the stored value, reject it and flag the credential as potentially cloned. Log this event

#### 2FA Reset Permissions

Resetting 2FA means: set `must_setup_2fa = true`, clear `totp_secret`, set `totp_verified = false`, delete all `WebAuthnCredentials` rows for the user, clear `recovery_codes`. On next login the user is forced through the full setup flow again, choosing their method fresh.

| Who resets | Whose 2FA | Condition |
|------------|-----------|-----------|
| superadmin | Anyone | No restrictions |
| staff | Any client user | No restrictions |
| staff | Other staff | Not allowed (only superadmin) |
| client (org owner/admin) | Members of their own org | Must be `owner` or `admin` role in the OrgMembership |
| client (member) | Anyone | Not allowed |
| Any user | Themselves | Allowed, but must verify current TOTP code first to confirm identity |

**Reset flow for admins/staff:**
1. Navigate to user management (org settings for client admins, admin panel for staff/superadmin)
2. Click "Reset 2FA" on a user row
3. Confirm action (hx-confirm dialog)
4. System clears TOTP secret and recovery codes, sets must_setup_2fa = true
5. Activity logged in audit trail with who reset whose 2FA and when
6. On next login, affected user is forced through 2FA setup again

**Self-reset flow:**
1. User goes to their account settings → Security
2. Click "Reset all 2FA methods"
3. Must verify identity with current TOTP code OR WebAuthn assertion
4. System clears all methods and forces re-setup on next login (user is logged out immediately)

**Individual credential management (no full reset needed):**
- Users can add additional WebAuthn credentials or set up TOTP from `/account/security` at any time
- Users can remove individual WebAuthn credentials — unless it's their only remaining 2FA method
- Users can disable TOTP — unless they have no WebAuthn credentials registered
- Rule: `count(webauthn_credentials) + (totp_verified ? 1 : 0) >= 1` must always hold

#### Session Security
- Session tokens: 256-bit cryptographically random, stored server-side in DB (not JWT)
- Session lifetime: 24 hours, sliding expiration on activity
- Concurrent sessions allowed (user might be on phone + desktop)
- On 2FA reset: all active sessions for that user are invalidated immediately
- On password change: all other sessions invalidated
- Secure cookie flags: `HttpOnly`, `Secure`, `SameSite=Strict`, `Path=/`

---

## Orchestrator Design

### State Machine (Ticket Lifecycle)

```
backlog ──→ ready ──→ planning ──→ plan_review ──→ implementing ──→ testing ──→ review ──→ done
  │                      │              │                │              │          │
  │                      │              ▼                │              │          ▼
  └──────────────────────┴──────── cancelled ◄───────────┴──────────────┘      merged/deployed
```

**Transitions triggered by:**
- `backlog → ready`: Client or staff moves ticket to ready
- `ready → planning`: Staff sets `agent_mode = plan`, orchestrator picks it up
- `planning → plan_review`: Agent finishes, posts plan as comment
- `plan_review → implementing`: Staff approves plan, sets `agent_mode = implement`
- `implementing → testing`: Agent finishes, orchestrator deploys to test env
- `testing → review`: Client/staff confirms testing looks good
- `review → done`: Staff triggers merge to main branch + production deploy

### Orchestrator Loop (cmd/orchestrator/main.go)

```
every 10 seconds:
  1. Query tickets WHERE agent_mode IS NOT NULL AND status IN ('ready', 'plan_review_approved')
  2. For each ticket:
     a. Assemble context (project brief, ticket description, parent epic, related tickets, codebase)
     b. Determine agent (default: claude, override via agent_name)
     c. Dispatch:
        - If agent_mode == 'plan': ask agent to produce implementation plan, post as comment, set status = plan_review
        - If agent_mode == 'implement': ask agent to implement, commit to branch, set status = testing, trigger deploy
  3. On agent completion:
     a. Parse output
     b. Create comment with results
     c. Transition ticket status
     d. If implementation: create git branch, commit, open PR, deploy to test env
```

### Agent Interface

```go
type Agent interface {
    Plan(ctx context.Context, req AgentRequest) (PlanResult, error)
    Implement(ctx context.Context, req AgentRequest) (ImplementResult, error)
}

type AgentRequest struct {
    ProjectBrief    string            // Markdown brief
    TicketTitle     string
    TicketDesc      string
    ParentEpic      *TicketSummary    // If this is a task under an epic
    RelatedTickets  []TicketSummary   // Other tickets in the project for context
    PlanComment     string            // Approved plan (for implement phase)
    RepoPath        string            // Local path to the project repo
    BranchName      string            // Branch to work on
    ExtraContext    map[string]string  // Per-project CLAUDE.md, conventions, etc.
}
```

### Agent Implementations

- **Claude (primary):** Shell out to `claude` CLI with `--print` for plan mode. For implementation, use `claude` in interactive/agentic mode pointed at the repo. Claude Code can create files, run tests, commit.
- **Gemini / Codex / Mistral:** API-based calls for lighter tasks (documentation, test generation, code review, translations). Implement the same `Agent` interface.

### Test Environment Provisioning

When a ticket moves to `testing`:
1. Create/update a K8s namespace: `forgedesk-{org_slug}-{project_slug}-test`
2. Create a TLS Secret in the namespace from `smart.madalin.me.crt` and `smart.madalin.me.key`
3. Create a database for the project on the managed PostgreSQL instance (credentials from `.secrets`)
4. Apply manifests from `deploy/templates/` with project-specific values
5. Set up Ingress with `{project_slug}-test.{org_slug}.smart.madalin.me` using the TLS secret
6. Deploy the agent's built artifact (container image) to the namespace
7. Post the test URL as a comment on the ticket

### Kubernetes Deployment (ForgeDesk itself)

ForgeDesk runs on K8s alongside the projects it manages:

```
Namespace: forgedesk
├── Deployment: forgedesk-server     (the web app)
├── Deployment: forgedesk-orchestrator (the agent dispatcher)
├── Service: forgedesk-server        (ClusterIP → port 8080)
├── Ingress: forgedesk               (forgedesk.smart.madalin.me, TLS)
├── Secret: tls-smart-madalin-me     (from smart.madalin.me.crt + .key)
├── Secret: forgedesk-db             (postgres credentials from .secrets)
├── Secret: forgedesk-agents         (API keys for Gemini/Codex/Mistral)
└── ConfigMap: forgedesk-config      (non-sensitive app config)
```

### Secrets Management

The `.secrets` file lives on the VM only, never in git. Sourceable shell format:
```bash
export POSTGRES_HOST=...
export POSTGRES_PORT=5432
export POSTGRES_USER=forgedesk
export POSTGRES_PASSWORD=...
export POSTGRES_DB=forgedesk
export DATABASE_URL=postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@${POSTGRES_HOST}:${POSTGRES_PORT}/${POSTGRES_DB}?sslmode=require
export AES_ENCRYPTION_KEY=...          # For TOTP secret encryption at rest (openssl rand -hex 32)
export SESSION_SECRET=...              # For session cookie signing (openssl rand -base64 32)
export GEMINI_API_KEY=...
export CODEX_API_KEY=...
export MISTRAL_API_KEY=...
```

K8s secrets are created from this file:
```bash
# Strip 'export ' prefix for kubectl compatibility
grep -v '^#' .secrets | sed 's/^export //' > /tmp/secrets.env

kubectl create secret generic forgedesk-db \
  --from-env-file=/tmp/secrets.env \
  -n forgedesk --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret tls tls-smart-madalin-me \
  --cert=smart.madalin.me.crt \
  --key=smart.madalin.me.key \
  -n forgedesk --dry-run=client -o yaml | kubectl apply -f -

rm /tmp/secrets.env
```

Per-project test databases are provisioned by the orchestrator:
```bash
# Orchestrator creates a DB per project on the managed Postgres instance
psql -h $POSTGRES_HOST -U $POSTGRES_USER -c "CREATE DATABASE \"forgedesk_${org_slug}_${project_slug}_test\";"
```

---

## UI / UX Guidelines

### General Principles
- Clean, professional, minimal. No visual clutter
- Use HTMX for all dynamic interactions: tab switching, ticket updates, comment posting, status changes. Avoid full page reloads
- Alpine.js only for client-side-only behavior: modal open/close, dropdown toggles, form validation hints, tab state
- No JavaScript frameworks. No React, no Vue, no Svelte. HTMX + Alpine.js is the entire frontend stack
- Mobile responsive but desktop-first (clients use this at work)

### Page Structure
- Left sidebar: Org switcher (if user belongs to multiple orgs), project list
- Top bar: User menu, notifications
- Main content: Tabbed interface per project

### Project Tabs (Client View)
1. **Brief** — Rendered markdown. Staff can edit. Clients can view. This is the project's living spec document
2. **Features** — List of Epics. Click an Epic to expand its Tasks. Click a Task to see detail (description, comments, dates, status). Subtasks visible only inside a Task's detail view
3. **Bugs** — Flat list of bug tickets. Same detail view pattern as tasks
4. **Timeline** — D3-based Gantt chart showing all tickets that have `date_start` and `date_end` set. Epics as grouped bars, tasks as individual bars within. Bugs in a separate swim lane. Color-coded by status

### Ticket Detail View (HTMX partial, slides in or modal)
- Title, description (rendered markdown)
- Status badge, priority badge
- Date range (start → end)
- Assigned to
- Comments thread (newest at bottom)
- Post comment form (HTMX submit, appends to thread)
- For staff: agent controls (mode selector, agent selector, approve/reject plan, trigger merge)

### HTMX Patterns to Follow
```html
<!-- Tab switching without page reload -->
<div hx-get="/projects/{{.Project.Slug}}/features" hx-target="#tab-content" hx-swap="innerHTML">
  Features
</div>

<!-- Post comment, append to list -->
<form hx-post="/tickets/{{.Ticket.ID}}/comments" hx-target="#comments" hx-swap="beforeend">
  <textarea name="body"></textarea>
  <button type="submit">Comment</button>
</form>

<!-- Status change with confirmation -->
<button hx-patch="/tickets/{{.Ticket.ID}}/status"
        hx-vals='{"status": "implementing"}'
        hx-confirm="Approve this plan and start implementation?"
        hx-target="#ticket-header">
  Approve Plan
</button>
```

---

## Coding Conventions

### Go
- Use Go standard library where possible. Avoid unnecessary dependencies
- Errors are returned, not panicked. Wrap errors with `fmt.Errorf("doing thing: %w", err)`
- Use `context.Context` throughout. Pass it from HTTP handlers to DB queries
- Database queries: use `pgx` directly (no ORM). Write SQL in the Go files or in a `queries/` directory
- Keep handlers thin. Business logic lives in `internal/` packages, not in handlers
- Use struct embedding for common fields (timestamps, soft-delete)
- Naming: `snake_case` for DB columns, `CamelCase` for Go. Use `db:"column_name"` struct tags

### Templates
- Use Go template inheritance: `{{template "base" .}}` with `{{define "content"}}`
- HTMX partials are standalone template files that render fragments (no layout wrapping)
- Always set appropriate `hx-target` and `hx-swap` — never rely on defaults
- Use `hx-indicator` for loading states on slower operations

### CSS / Tailwind
- Use Tailwind utility classes directly in templates
- Build with standalone Tailwind CLI (no npm): `tailwindcss -i static/css/input.css -o static/css/app.css`
- Keep a small `input.css` with `@tailwind base; @tailwind components; @tailwind utilities;` and any custom component classes
- Dark mode: not required for MVP, but use Tailwind's `dark:` prefix if adding later

### JavaScript
- Vanilla JS only. No transpilation, no bundling, no npm
- D3 code lives in `static/js/gantt.js` as a self-contained module
- WebAuthn client code lives in `static/js/webauthn.js` — wraps `navigator.credentials.create()` and `navigator.credentials.get()`, handles ArrayBuffer↔Base64URL encoding, communicates with server endpoints via fetch
- Alpine.js components are declared inline in templates with `x-data`
- HTMX extensions loaded as needed (e.g., `hx-ext="json-enc"` for JSON request bodies)

### Database
- All tables have `id` (UUID), `created_at`, `updated_at`
- Use `ON DELETE CASCADE` for child entities (comments, activities)
- Index all foreign keys and commonly filtered columns (status, type, org_id, project_id)
- Migration files named: `000001_create_users.up.sql`, `000001_create_users.down.sql`

### Git Conventions
- Agent work branches: `agent/{ticket-id}-{short-slug}`
- Human branches: `feature/`, `fix/`, `chore/`
- Commit messages: `[TICKET-ID] description` (e.g., `[T-42] implement user login flow`)
- PRs require at least one staff approval before merge

---

## Container Build

Use `Containerfile` (Podman), not `Dockerfile`:

```containerfile
FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o forgedesk ./cmd/server
RUN CGO_ENABLED=0 go build -o orchestrator ./cmd/orchestrator

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/forgedesk /usr/local/bin/
COPY --from=builder /app/orchestrator /usr/local/bin/
COPY --from=builder /app/templates /app/templates
COPY --from=builder /app/static /app/static
COPY --from=builder /app/migrations /app/migrations
WORKDIR /app
```

Build with: `podman build -t forgedesk:latest -f deploy/Containerfile .`

---

## Development Workflow

1. `source .secrets` — load environment variables (Postgres creds, API keys, encryption keys)
2. `go run ./cmd/server` — starts web app on :8080
3. `go run ./cmd/orchestrator` — starts agent dispatcher (separate process)
4. `tailwindcss -w -i static/css/input.css -o static/css/app.css` — watch mode for CSS
5. Run migrations: `migrate -database $DATABASE_URL -path migrations up`

---

## Non-Goals (for now)

- Real-time websockets (HTMX polling is fine for MVP)
- Email notifications (can be added later)
- File uploads on tickets (future enhancement)
- Multi-language i18n (English only for now)
- Billing / invoicing (out of scope)
- Public API (internal use only)

---

## Security Considerations

- All routes behind AuthMiddleware (enforces both password auth AND 2FA verification) except: `/login`, `/setup-2fa`, `/verify-2fa`, `/static/*`
- 2FA is mandatory. No user can access any application functionality without verified 2FA (TOTP or WebAuthn)
- Users cannot remove their last 2FA method — at least one must remain active
- WebAuthn: validate origin and RPID strictly. Reject any assertion where sign_count does not increment (cloned key detection). Challenges are single-use, stored in session, expire after 60 seconds
- CSRF protection on all state-changing requests (use `gorilla/csrf` or custom double-submit cookie). WebAuthn endpoints are exempt from CSRF (the challenge-response protocol is inherently CSRF-safe) but must validate origin
- Rate limiting: login attempts (5/15min per IP+email), 2FA verification (5 attempts then 15min lockout), API endpoints
- Client users can only access their own org's data — enforce at query level with `WHERE org_id = $user_org_id`, never trust client-side org context alone
- Agent credentials (API keys for Gemini/Codex/Mistral) stored in `.secrets` file on the VM, injected as K8s Secrets, never in code or git
- `.secrets`, `smart.madalin.me.crt`, and `smart.madalin.me.key` must be in `.gitignore` — fail CI if these are ever staged
- TOTP secrets and recovery codes encrypted at rest in the database (AES-256-GCM, key from env var)
- WebAuthn credential public keys stored as raw bytes — no encryption needed (they are public), but credential_id is indexed and unique
- All 2FA resets logged to audit trail (who, whose, when, from which IP)
- Test environment namespaces are isolated in K8s with NetworkPolicy
- Session tokens are server-side (DB-stored), 256-bit random, never exposed as JWT
- On any security-sensitive change (password, 2FA reset): invalidate all sessions for the affected user
