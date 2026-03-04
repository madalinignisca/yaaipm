ALTER TABLE sessions ADD COLUMN selected_org_id UUID REFERENCES organizations(id) ON DELETE SET NULL;
