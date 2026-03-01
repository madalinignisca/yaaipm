# ForgeDesk — Project Brief

## 🎯 What Is ForgeDesk?

ForgeDesk is a **self-hosted service desk and project management platform** purpose-built for software consulting workflows. It bridges the gap between **non-technical clients** and **AI-powered development agents**.

Clients see a clean, simple project management tool. Behind the scenes, an intelligent orchestrator dispatches work to AI coding agents, deploys results to isolated test environments, and awaits human approval before merging to production.

---

## 🧩 How It Works

```
Client creates ticket → Staff approves → AI agent plans & builds →
Auto-deploys to test env → Client reviews → Staff merges to production
```

1. **Clients** describe what they need through project briefs, feature requests, and bug reports
2. **Staff** reviews requests, sets priorities, and assigns AI agents
3. **AI Agents** (Claude, Gemini, Codex, Mistral) generate implementation plans and write code
4. **Test environments** are provisioned automatically on Kubernetes for review
5. **Staff approves** the result and triggers production deployment

---

## 👥 User Roles

| Role | Who | What They Do |
|------|-----|-------------|
| 🔑 **Superadmin** | Platform owner | Full control — orgs, users, agents, deployments |
| 🛠️ **Staff** | Internal team | Assign agents, approve plans, trigger deploys |
| 👤 **Client** | External customers | Manage projects, create tickets, review deliverables |

> Clients never see AI internals — agent comments appear as "ForgeDesk Bot"

---

## 📋 Key Features

### ✅ Delivered (MVP — Live)

- **🔐 Authentication & Security** — Mandatory 2FA (TOTP), Argon2id password hashing, server-side sessions, role-based access control
- **🏢 Organizations & Projects** — Multi-tenant with org membership, project briefs, per-org settings
- **📝 Ticket Management** — Feature requests, bugs, sub-tasks with full lifecycle tracking (backlog → done)
- **💬 Comments & Reactions** — Threaded discussions on tickets with emoji reactions
- **🤖 AI Chat Assistant** — Gemini-powered conversational assistant with streaming responses and function calling (search tickets, update status, create items)
- **🖼️ Image Management** — Unified modal for uploading images or generating them with AI, integrated into the Markdown editor
- **📎 File Attachments** — S3-backed file storage with upload/download on tickets
- **💰 Display Currency** — Per-organization currency setting for financial context
- **🚀 Kubernetes Deployment** — Running on k3s cluster with automated container builds and rollouts

### 🔜 Planned / In Progress

- **🤖 Agent Orchestrator** — Automated dispatch of coding work to AI agents (framework built, execution stubbed)
- **🔑 WebAuthn / Passkeys** — Hardware security key support (UI stubbed, backend ready)
- **📊 Gantt Chart** — D3.js timeline visualization (placeholder in place)
- **🌐 Test Environment Provisioning** — Auto-deploy agent work to per-project K8s namespaces for client review

---

## 🏗️ Architecture Overview

```
┌──────────────┐     ┌──────────────────┐     ┌─────────────────┐
│   Clients    │────▶│   ForgeDesk Web   │────▶│   PostgreSQL    │
│  (Browser)   │◀────│   (Go + HTMX)    │◀────│   (Data Store)  │
└──────────────┘     └──────────────────┘     └─────────────────┘
                              │
                     ┌────────▼─────────┐     ┌─────────────────┐
                     │   Orchestrator   │────▶│  AI Agents      │
                     │  (Job Dispatch)  │     │  Claude, Gemini │
                     └──────────────────┘     │  Codex, Mistral │
                              │               └─────────────────┘
                     ┌────────▼─────────┐
                     │   Kubernetes     │
                     │  (Test Envs)     │
                     └──────────────────┘
```

- **Backend:** Go with server-side rendering (no heavy JS frameworks)
- **Frontend:** HTMX + Alpine.js — fast, lightweight, no build step
- **Storage:** PostgreSQL + S3 (MinIO-compatible)
- **Infra:** Kubernetes (k3s), container-based deployments
- **Domain:** `smart.madalin.me` with TLS

---

## 🔒 Security Posture

- ✅ Mandatory 2FA for all users — no exceptions, no skip button
- ✅ Argon2id password hashing (GPU/ASIC resistant)
- ✅ AES-256-GCM encryption for secrets at rest
- ✅ Server-side sessions (no JWT exposure)
- ✅ CSRF protection on all state-changing requests
- ✅ Rate limiting on login and 2FA verification
- ✅ Org-level data isolation enforced at the query level

---

## 📈 Current Status

| Area | Status |
|------|--------|
| Core Platform (auth, orgs, projects, tickets) | ✅ **Production** |
| AI Chat Assistant | ✅ **Production** |
| Image Upload & AI Generation | ✅ **Production** |
| E2E Test Coverage | ✅ **117 tests across 10 suites** |
| Agent Orchestrator | 🟡 **Framework built, execution pending** |
| WebAuthn / Passkeys | 🟡 **Backend ready, UI pending** |
| Gantt Timeline | 🟡 **Placeholder** |

---

## 💡 Value Proposition

> **For consulting firms:** Offer clients a professional project portal while leveraging AI agents to dramatically accelerate delivery. Clients interact with a familiar PM tool — they don't need to know AI is doing the heavy lifting.

> **For internal teams:** Reduce context-switching. Define the work, let AI agents draft plans and write code, review the output, and ship. Human judgment stays in the loop at every critical decision point.
