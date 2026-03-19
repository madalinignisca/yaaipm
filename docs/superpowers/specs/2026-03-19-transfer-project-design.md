# Transfer Project Between Organizations

## Summary

Staff and superadmins can move a project (and all its children) from one org to another via the Project Settings page. The operation updates `projects.org_id` and redirects to the new org-scoped URL.

## Data Model

Moving a project requires updating one column: `projects.org_id`. All child tables (tickets, comments, ticket_activities, ticket_attachments, ai_conversations, ai_messages, project_costs, brief_revisions) reference `project_id` not `org_id`, so they survive the move intact.

Exception: `ai_usage_entries` has both `org_id` and `project_id`. These must be updated to keep cost attribution consistent.

Constraint: `UNIQUE(org_id, slug)` on projects — if the target org already has a project with the same slug, the move is blocked with an error message.

## Access Control

Staff and superadmin only (matches existing project settings access). The handler verifies `auth.IsStaffOrAbove(user.Role)` before processing.

## UI

A "Transfer Project" section at the bottom of the existing Project Settings page. Contains:
- A `<select>` dropdown listing all orgs except the current one
- A "Transfer" button
- An Alpine.js confirmation modal before submission

## Backend

### New query: `TransferProject`

```sql
UPDATE projects SET org_id = $1, updated_at = now() WHERE id = $2
UPDATE ai_usage_entries SET org_id = $1 WHERE project_id = $2
```

Returns error on slug collision (unique constraint violation).

### New handler: `TransferProject`

- Route: `POST /orgs/{orgSlug}/projects/{projSlug}/transfer`
- Validates staff/superadmin role
- Validates target org exists
- Calls `TransferProject` query
- On success: redirects to `/orgs/{newOrgSlug}/projects/{projSlug}/settings` with flash message
- On slug collision: redirects back with error flash

## Edge Cases

- Slug collision: block with error, no auto-rename
- After transfer: user redirected to project's new URL
- No schema migration needed

## Not in Scope

- Audit trail table
- Notifications
- Undo functionality
