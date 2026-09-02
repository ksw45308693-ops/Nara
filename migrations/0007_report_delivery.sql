CREATE TABLE public.reports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id uuid NOT NULL REFERENCES public.tenants(id) ON DELETE CASCADE,
    schedule_id uuid,
    due_at timestamptz NOT NULL,
    window_start_at timestamptz,
    window_end_at timestamptz NOT NULL,
    trigger text NOT NULL CHECK (trigger IN ('scheduled', 'manual')),
    status text NOT NULL CHECK (status IN ('generating', 'generated', 'failed')),
    relative_path text NOT NULL DEFAULT '',
    sha256 text NOT NULL DEFAULT '',
    notice_count integer NOT NULL DEFAULT 0 CHECK (notice_count >= 0),
    attempts smallint NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
    last_error text,
    claim_token uuid,
    claimed_at timestamptz,
    generated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (window_start_at IS NULL OR window_start_at <= window_end_at),
    CHECK ((trigger = 'scheduled' AND schedule_id IS NOT NULL) OR (trigger = 'manual' AND schedule_id IS NULL)),
    CONSTRAINT reports_digest_window_fk
        FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at)
        REFERENCES public.digest_windows (tenant_id, schedule_id, due_at, window_end_at),
    UNIQUE (tenant_id, id)
);

CREATE UNIQUE INDEX reports_scheduled_due_unique
    ON public.reports (tenant_id, schedule_id, due_at)
    WHERE trigger = 'scheduled';
CREATE INDEX reports_tenant_status_due_idx
    ON public.reports (tenant_id, status, due_at);

CREATE TABLE public.report_items (
    report_id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    match_id uuid NOT NULL,
    notice_id uuid NOT NULL,
    title text NOT NULL,
    category text NOT NULL CHECK (category IN ('construction', 'service', 'goods', 'foreign')),
    agency text NOT NULL DEFAULT '',
    region text NOT NULL DEFAULT '',
    amount bigint NOT NULL DEFAULT 0 CHECK (amount >= 0),
    deadline_at timestamptz NOT NULL,
    source_url text NOT NULL DEFAULT '',
    rule_name text NOT NULL DEFAULT '',
    reasons jsonb NOT NULL,
    CONSTRAINT report_items_report_fk
        FOREIGN KEY (tenant_id, report_id)
        REFERENCES public.reports (tenant_id, id) ON DELETE CASCADE,
    UNIQUE (tenant_id, report_id, ordinal),
    UNIQUE (tenant_id, report_id, match_id)
);
CREATE INDEX report_items_tenant_report_ordinal_idx
    ON public.report_items (tenant_id, report_id, ordinal);

ALTER TABLE public.reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.reports FORCE ROW LEVEL SECURITY;
CREATE POLICY reports_tenant_isolation ON public.reports
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

ALTER TABLE public.report_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.report_items FORCE ROW LEVEL SECURITY;
CREATE POLICY report_items_tenant_isolation ON public.report_items
    USING (tenant_id = current_setting('app.tenant_id', true)::uuid)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid);

REVOKE ALL ON TABLE public.reports, public.report_items FROM PUBLIC, namo_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE public.reports TO namo_runtime;
GRANT SELECT, INSERT ON TABLE public.report_items TO namo_runtime;
