ALTER TABLE public.reports ADD COLUMN tenant_name text;
ALTER TABLE public.reports ADD COLUMN schedule_name text;

UPDATE public.reports r
SET tenant_name = COALESCE(
        (SELECT NULLIF(pg_catalog.btrim(t.name), '')
         FROM public.tenants t
         WHERE t.id = r.tenant_id),
        r.tenant_id::text
    ),
    schedule_name = CASE
        WHEN r.trigger = 'manual' THEN '수동'
        ELSE COALESCE(
            (SELECT NULLIF(pg_catalog.btrim(s.name), '')
             FROM public.schedules s
             WHERE s.tenant_id = r.tenant_id AND s.id = r.schedule_id),
            r.schedule_id::text
        )
    END;

ALTER TABLE public.reports ALTER COLUMN tenant_name SET NOT NULL;
ALTER TABLE public.reports ALTER COLUMN schedule_name SET NOT NULL;
ALTER TABLE public.reports ADD CONSTRAINT reports_tenant_name_nonblank
    CHECK (btrim(tenant_name) <> '');
ALTER TABLE public.reports ADD CONSTRAINT reports_schedule_name_nonblank
    CHECK (btrim(schedule_name) <> '');

GRANT SELECT, INSERT, UPDATE ON TABLE public.reports TO namo_runtime;
