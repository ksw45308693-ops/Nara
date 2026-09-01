CREATE EXTENSION IF NOT EXISTS pgcrypto;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'g2b_runtime') THEN
        CREATE ROLE g2b_runtime NOLOGIN NOBYPASSRLS;
    END IF;
END $$;
ALTER ROLE g2b_runtime NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'g2b_auth_definer') THEN
        CREATE ROLE g2b_auth_definer NOLOGIN BYPASSRLS NOINHERIT;
    END IF;
END $$;
ALTER ROLE g2b_auth_definer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS NOINHERIT;

DO $$
DECLARE parent_role record;
BEGIN
    FOR parent_role IN
        SELECT parent.rolname
        FROM pg_catalog.pg_auth_members membership
        JOIN pg_catalog.pg_roles parent ON parent.oid = membership.roleid
        JOIN pg_catalog.pg_roles child ON child.oid = membership.member
        WHERE child.rolname = 'g2b_runtime'
    LOOP
        EXECUTE format('REVOKE %I FROM g2b_runtime', parent_role.rolname);
    END LOOP;
END $$;

DO $$
DECLARE parent_role record;
BEGIN
    FOR parent_role IN
        SELECT parent.rolname
        FROM pg_catalog.pg_auth_members membership
        JOIN pg_catalog.pg_roles parent ON parent.oid = membership.roleid
        JOIN pg_catalog.pg_roles child ON child.oid = membership.member
        WHERE child.rolname = 'g2b_auth_definer'
    LOOP
        EXECUTE format('REVOKE %I FROM g2b_auth_definer', parent_role.rolname);
    END LOOP;
END $$;

CREATE TABLE tenants (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(trim(name)) > 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid REFERENCES tenants(id) ON DELETE CASCADE,
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    role text NOT NULL CHECK (role IN ('platform_admin', 'tenant_admin', 'member')),
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((role = 'platform_admin') = (tenant_id IS NULL))
);
CREATE INDEX users_tenant_id_idx ON users (tenant_id);

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE notices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    identity_hash bytea NOT NULL UNIQUE,
    revision_hash bytea NOT NULL,
    source_id text NOT NULL,
    title text NOT NULL,
    published_at timestamptz,
    deadline_at timestamptz,
    payload jsonb NOT NULL,
    collected_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notices_deadline_at_idx ON notices (deadline_at);
CREATE INDEX notices_published_at_idx ON notices (published_at DESC);

CREATE TABLE filters (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(trim(name)) > 0),
    rules jsonb NOT NULL DEFAULT '{}'::jsonb,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    UNIQUE (tenant_id, id)
);
CREATE INDEX filters_tenant_enabled_idx ON filters (tenant_id, enabled);

CREATE TABLE matches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    filter_id uuid NOT NULL,
    notice_id uuid NOT NULL REFERENCES notices(id) ON DELETE CASCADE,
    reasons jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT matches_filter_tenant_fk FOREIGN KEY (tenant_id, filter_id) REFERENCES filters (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, filter_id, notice_id)
);
CREATE INDEX matches_tenant_created_idx ON matches (tenant_id, created_at DESC);
CREATE INDEX matches_notice_id_idx ON matches (notice_id);

CREATE TABLE schedules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (length(trim(name)) > 0),
    hour smallint NOT NULL CHECK (hour BETWEEN 0 AND 23),
    minute smallint NOT NULL CHECK (minute BETWEEN 0 AND 59),
    timezone text NOT NULL DEFAULT 'Asia/Seoul' CHECK (timezone = 'Asia/Seoul'),
    enabled boolean NOT NULL DEFAULT true,
    last_success_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name),
    UNIQUE (tenant_id, id)
);
CREATE INDEX schedules_tenant_enabled_idx ON schedules (tenant_id, enabled);

CREATE TABLE recipients (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email),
    UNIQUE (tenant_id, id)
);
CREATE INDEX recipients_tenant_enabled_idx ON recipients (tenant_id, enabled);

CREATE TABLE deliveries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    schedule_id uuid NOT NULL,
    recipient_id uuid NOT NULL,
    idempotency_key text NOT NULL,
    due_at timestamptz NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'sending', 'sent', 'failed')),
    attempts smallint NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    last_error text,
    sent_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deliveries_schedule_tenant_fk FOREIGN KEY (tenant_id, schedule_id) REFERENCES schedules (tenant_id, id) ON DELETE CASCADE,
    CONSTRAINT deliveries_recipient_tenant_fk FOREIGN KEY (tenant_id, recipient_id) REFERENCES recipients (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX deliveries_tenant_status_idx ON deliveries (tenant_id, status, due_at);
CREATE INDEX deliveries_recipient_id_idx ON deliveries (recipient_id);

CREATE TABLE job_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid REFERENCES tenants(id) ON DELETE CASCADE,
    kind text NOT NULL,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX job_runs_tenant_started_idx ON job_runs (tenant_id, started_at DESC);

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE filters ENABLE ROW LEVEL SECURITY;
ALTER TABLE matches ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE recipients ENABLE ROW LEVEL SECURITY;
ALTER TABLE deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_runs ENABLE ROW LEVEL SECURITY;

ALTER TABLE users FORCE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
ALTER TABLE filters FORCE ROW LEVEL SECURITY;
ALTER TABLE matches FORCE ROW LEVEL SECURITY;
ALTER TABLE schedules FORCE ROW LEVEL SECURITY;
ALTER TABLE recipients FORCE ROW LEVEL SECURITY;
ALTER TABLE deliveries FORCE ROW LEVEL SECURITY;
ALTER TABLE job_runs FORCE ROW LEVEL SECURITY;

CREATE POLICY users_tenant_isolation ON users USING (tenant_id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
CREATE POLICY tenants_tenant_isolation ON tenants USING (id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (id = current_setting('app.tenant_id', true)::uuid);
CREATE POLICY filters_tenant_isolation ON filters USING (tenant_id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
CREATE POLICY matches_tenant_isolation ON matches USING (tenant_id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
CREATE POLICY schedules_tenant_isolation ON schedules USING (tenant_id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
CREATE POLICY recipients_tenant_isolation ON recipients USING (tenant_id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
CREATE POLICY deliveries_tenant_isolation ON deliveries USING (tenant_id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);
CREATE POLICY job_runs_tenant_isolation ON job_runs USING (tenant_id = current_setting('app.tenant_id', true)::uuid) WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM g2b_runtime;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM g2b_runtime;
GRANT USAGE ON SCHEMA public TO g2b_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.tenants, public.users, public.filters, public.matches, public.schedules, public.recipients, public.deliveries, public.job_runs TO g2b_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE public.notices TO g2b_runtime;

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM g2b_auth_definer;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM g2b_auth_definer;
GRANT USAGE ON SCHEMA public TO g2b_auth_definer;
GRANT SELECT (id, tenant_id, email, password_hash, role) ON TABLE public.users TO g2b_auth_definer;
GRANT SELECT (user_id, token_hash, expires_at) ON TABLE public.sessions TO g2b_auth_definer;
GRANT INSERT (user_id, token_hash, expires_at), DELETE ON TABLE public.sessions TO g2b_auth_definer;

CREATE FUNCTION auth_account_lookup(p_email text)
RETURNS TABLE (user_id uuid, tenant_id uuid, email text, password_hash text, role text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT u.id, u.tenant_id, u.email, u.password_hash, u.role
    FROM public.users u
    WHERE u.email = p_email
$$;

CREATE FUNCTION auth_session_lookup(p_token_hash text)
RETURNS TABLE (user_id uuid, tenant_id uuid, email text, role text, expires_at timestamptz)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT s.user_id, u.tenant_id, u.email, u.role, s.expires_at
    FROM public.sessions s
    JOIN public.users u ON u.id = s.user_id
    WHERE s.token_hash = p_token_hash AND s.expires_at > now()
$$;

CREATE FUNCTION auth_session_create(p_user_id uuid, p_token_hash text, p_expires_at timestamptz)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF coalesce(length(btrim(p_token_hash)), 0) = 0 THEN
        RAISE EXCEPTION 'session token hash is required';
    END IF;
    IF p_expires_at IS NULL OR p_expires_at <= now() OR p_expires_at > now() + interval '90 days' THEN
        RAISE EXCEPTION 'session expiry must be future and within 90 days';
    END IF;
    INSERT INTO public.sessions (user_id, token_hash, expires_at)
    VALUES (p_user_id, p_token_hash, p_expires_at);
END;
$$;

CREATE FUNCTION auth_session_delete(p_token_hash text)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE deleted_count integer;
BEGIN
    IF coalesce(length(btrim(p_token_hash)), 0) = 0 THEN
        RAISE EXCEPTION 'session token hash is required';
    END IF;
    DELETE FROM public.sessions WHERE token_hash = p_token_hash;
    GET DIAGNOSTICS deleted_count = ROW_COUNT;
    RETURN deleted_count = 1;
END;
$$;

ALTER FUNCTION auth_account_lookup(text) OWNER TO g2b_auth_definer;
ALTER FUNCTION auth_session_lookup(text) OWNER TO g2b_auth_definer;
ALTER FUNCTION auth_session_create(uuid, text, timestamptz) OWNER TO g2b_auth_definer;
ALTER FUNCTION auth_session_delete(text) OWNER TO g2b_auth_definer;
REVOKE ALL ON FUNCTION auth_account_lookup(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION auth_session_lookup(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION auth_session_create(uuid, text, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION auth_session_delete(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth_account_lookup(text) TO g2b_runtime;
GRANT EXECUTE ON FUNCTION auth_session_lookup(text) TO g2b_runtime;
GRANT EXECUTE ON FUNCTION auth_session_create(uuid, text, timestamptz) TO g2b_runtime;
GRANT EXECUTE ON FUNCTION auth_session_delete(text) TO g2b_runtime;
