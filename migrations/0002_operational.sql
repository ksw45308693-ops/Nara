ALTER TABLE public.tenants ADD COLUMN contact_email text NOT NULL DEFAULT '';
ALTER TABLE public.users ADD COLUMN display_name text NOT NULL DEFAULT '';
ALTER TABLE public.schedules ADD COLUMN weekdays smallint[] NOT NULL DEFAULT ARRAY[0,1,2,3,4,5,6]::smallint[]
    CHECK (cardinality(weekdays) > 0 AND weekdays <@ ARRAY[0,1,2,3,4,5,6]::smallint[]);
ALTER TABLE public.recipients ADD COLUMN name text NOT NULL DEFAULT '';
ALTER TABLE public.deliveries ADD COLUMN claimed_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE public.deliveries ADD COLUMN window_end_at timestamptz;
UPDATE public.deliveries SET window_end_at = due_at WHERE window_end_at IS NULL;
ALTER TABLE public.deliveries ALTER COLUMN window_end_at SET NOT NULL;
ALTER TABLE public.deliveries ADD COLUMN claim_token uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE public.deliveries ADD CONSTRAINT deliveries_window_bounds CHECK (window_end_at >= due_at);
ALTER TABLE public.deliveries ADD CONSTRAINT deliveries_recipient_window_unique
    UNIQUE (tenant_id, schedule_id, recipient_id, due_at);
ALTER TABLE public.deliveries DROP CONSTRAINT deliveries_recipient_tenant_fk;

CREATE TABLE public.notice_revisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    notice_id uuid NOT NULL REFERENCES public.notices(id) ON DELETE CASCADE,
    revision_hash bytea NOT NULL,
    payload jsonb NOT NULL,
    collected_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (notice_id, revision_hash)
);
CREATE INDEX notice_revisions_notice_collected_idx ON public.notice_revisions (notice_id, collected_at DESC);

CREATE TABLE public.source_warnings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    category text NOT NULL,
    page integer NOT NULL DEFAULT 0,
    item integer NOT NULL DEFAULT 0,
    field text NOT NULL,
    code text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    collected_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX source_warnings_collected_idx ON public.source_warnings (collected_at DESC);

CREATE TABLE public.collection_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    last_success_at timestamptz,
    last_result jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_error text,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO public.collection_state (singleton) VALUES (true) ON CONFLICT DO NOTHING;

CREATE TABLE public.digest_windows (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    schedule_id uuid NOT NULL,
    due_at timestamptz NOT NULL,
    window_end_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed', 'failed')),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (window_end_at >= due_at),
    CONSTRAINT digest_windows_schedule_tenant_fk
        FOREIGN KEY (tenant_id, schedule_id) REFERENCES public.schedules (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, schedule_id, due_at),
    UNIQUE (tenant_id, schedule_id, due_at, window_end_at)
);
CREATE INDEX digest_windows_tenant_status_idx ON public.digest_windows (tenant_id, status, due_at);

WITH grouped_windows AS (
    SELECT d.tenant_id, d.schedule_id, d.due_at, d.window_end_at,
           bool_and(d.status = 'sent') AS all_sent,
           max(d.sent_at) AS last_sent_at
    FROM public.deliveries d
    GROUP BY d.tenant_id, d.schedule_id, d.due_at, d.window_end_at
), ranked_windows AS (
    SELECT grouped_windows.*,
           row_number() OVER (
               PARTITION BY tenant_id, schedule_id
               ORDER BY due_at DESC, window_end_at DESC
           ) AS newest_rank
    FROM grouped_windows
)
INSERT INTO public.digest_windows
    (tenant_id, schedule_id, due_at, window_end_at, status, completed_at)
SELECT tenant_id, schedule_id, due_at, window_end_at,
       CASE
           WHEN all_sent THEN 'completed'
           WHEN newest_rank = 1 THEN 'pending'
           ELSE 'failed'
       END,
       CASE
           WHEN all_sent THEN last_sent_at
           WHEN newest_rank > 1 THEN now()
           ELSE NULL
       END
FROM ranked_windows
ON CONFLICT (tenant_id, schedule_id, due_at) DO NOTHING;

CREATE UNIQUE INDEX digest_windows_one_pending_per_schedule
    ON public.digest_windows (tenant_id, schedule_id)
    WHERE status = 'pending';

ALTER TABLE public.deliveries ADD CONSTRAINT deliveries_digest_window_fk
    FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at)
    REFERENCES public.digest_windows (tenant_id, schedule_id, due_at, window_end_at) ON DELETE CASCADE;

CREATE TABLE public.digest_window_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    schedule_id uuid NOT NULL,
    due_at timestamptz NOT NULL,
    window_end_at timestamptz NOT NULL,
    match_id uuid NOT NULL,
    notice_id uuid NOT NULL,
    title text NOT NULL,
    source_url text NOT NULL DEFAULT '',
    reasons jsonb NOT NULL,
    matched_at timestamptz NOT NULL,
    CHECK (window_end_at >= due_at),
    CONSTRAINT digest_window_items_window_fk
        FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at)
        REFERENCES public.digest_windows (tenant_id, schedule_id, due_at, window_end_at) ON DELETE CASCADE,
    UNIQUE (tenant_id, schedule_id, due_at, match_id)
);
CREATE INDEX digest_window_items_window_idx
    ON public.digest_window_items (tenant_id, schedule_id, due_at, title, notice_id);

INSERT INTO public.digest_window_items
    (tenant_id, schedule_id, due_at, window_end_at, match_id, notice_id, title, source_url, reasons, matched_at)
SELECT w.tenant_id, w.schedule_id, w.due_at, w.window_end_at, m.id, n.id, n.title,
       COALESCE(NULLIF(n.payload->>'SourceURL', ''), n.payload->>'source_url', ''),
       m.reasons, m.created_at
FROM public.digest_windows w
JOIN public.schedules s ON s.tenant_id = w.tenant_id AND s.id = w.schedule_id
JOIN public.matches m ON m.tenant_id = w.tenant_id
JOIN public.notices n ON n.id = m.notice_id
WHERE w.status = 'pending'
  AND m.created_at > COALESCE(s.last_success_at, '-infinity'::timestamptz)
  AND m.created_at <= w.window_end_at
ON CONFLICT (tenant_id, schedule_id, due_at, match_id) DO NOTHING;

CREATE TABLE public.digest_window_recipients (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL,
    schedule_id uuid NOT NULL,
    due_at timestamptz NOT NULL,
    window_end_at timestamptz NOT NULL,
    recipient_id uuid NOT NULL,
    email text NOT NULL,
    CHECK (window_end_at >= due_at),
    CONSTRAINT digest_window_recipients_window_fk
        FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at)
        REFERENCES public.digest_windows (tenant_id, schedule_id, due_at, window_end_at) ON DELETE CASCADE,
    UNIQUE (tenant_id, schedule_id, due_at, window_end_at, recipient_id)
);
CREATE INDEX digest_window_recipients_window_idx
    ON public.digest_window_recipients (tenant_id, schedule_id, due_at, email, recipient_id);

INSERT INTO public.digest_window_recipients
    (tenant_id, schedule_id, due_at, window_end_at, recipient_id, email)
SELECT d.tenant_id, d.schedule_id, d.due_at, d.window_end_at, d.recipient_id, r.email
FROM public.deliveries d
JOIN public.recipients r ON r.tenant_id = d.tenant_id AND r.id = d.recipient_id
ON CONFLICT (tenant_id, schedule_id, due_at, window_end_at, recipient_id) DO NOTHING;

ALTER TABLE public.deliveries ADD CONSTRAINT deliveries_digest_window_recipient_fk
    FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at, recipient_id)
    REFERENCES public.digest_window_recipients (tenant_id, schedule_id, due_at, window_end_at, recipient_id);

ALTER TABLE public.digest_windows ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.digest_windows FORCE ROW LEVEL SECURITY;
CREATE POLICY digest_windows_tenant_isolation ON public.digest_windows
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

ALTER TABLE public.digest_window_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.digest_window_items FORCE ROW LEVEL SECURITY;
CREATE POLICY digest_window_items_tenant_isolation ON public.digest_window_items
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

ALTER TABLE public.digest_window_recipients ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.digest_window_recipients FORCE ROW LEVEL SECURITY;
CREATE POLICY digest_window_recipients_tenant_isolation ON public.digest_window_recipients
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

CREATE TABLE public.invitations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    email text NOT NULL,
    role text NOT NULL CHECK (role IN ('tenant_admin', 'member')),
    token_hash text NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    accepted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);
ALTER TABLE public.invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.invitations FORCE ROW LEVEL SECURITY;
CREATE POLICY invitations_tenant_isolation ON public.invitations
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

GRANT SELECT, INSERT, UPDATE ON TABLE public.notice_revisions, public.source_warnings, public.collection_state TO namo_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE public.digest_windows TO namo_runtime;
GRANT SELECT, INSERT ON TABLE public.digest_window_items TO namo_runtime;
GRANT SELECT, INSERT ON TABLE public.digest_window_recipients TO namo_runtime;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.invitations TO namo_runtime;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'namo_catalog_definer') THEN
        CREATE ROLE namo_catalog_definer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS NOINHERIT;
    END IF;
END $$;
ALTER ROLE namo_catalog_definer NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION BYPASSRLS NOINHERIT;

DO $$
DECLARE parent_role record;
BEGIN
    FOR parent_role IN
        SELECT parent.rolname
        FROM pg_catalog.pg_auth_members membership
        JOIN pg_catalog.pg_roles parent ON parent.oid = membership.roleid
        JOIN pg_catalog.pg_roles member_role ON member_role.oid = membership.member
        WHERE member_role.rolname = 'namo_catalog_definer'
    LOOP
        EXECUTE format('REVOKE %I FROM namo_catalog_definer', parent_role.rolname);
    END LOOP;
END $$;

REVOKE ALL ON ALL TABLES IN SCHEMA public FROM namo_catalog_definer;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM namo_catalog_definer;
GRANT USAGE ON SCHEMA public TO namo_catalog_definer;
GRANT SELECT (id, name, contact_email, created_at) ON TABLE public.tenants TO namo_catalog_definer;

CREATE FUNCTION public.runtime_tenant_catalog()
RETURNS TABLE (tenant_id uuid, name text, contact_email text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT t.id, t.name, t.contact_email FROM public.tenants t ORDER BY t.created_at, t.id
$$;
ALTER FUNCTION public.runtime_tenant_catalog() OWNER TO namo_catalog_definer;
REVOKE ALL ON FUNCTION public.runtime_tenant_catalog() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.runtime_tenant_catalog() TO namo_runtime;
