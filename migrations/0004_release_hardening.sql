ALTER TABLE public.notices
    ADD COLUMN region_lookup_complete boolean NOT NULL DEFAULT false;
UPDATE public.notices
SET region_lookup_complete = coalesce(btrim(payload->>'Region'), '') <> '';
CREATE INDEX notices_region_lookup_pending_idx
    ON public.notices (collected_at) WHERE NOT region_lookup_complete;

CREATE TABLE public.api_daily_usage (
    usage_day date PRIMARY KEY,
    calls integer NOT NULL DEFAULT 0 CHECK (calls BETWEEN 0 AND 1000),
    updated_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON TABLE public.api_daily_usage FROM PUBLIC;
GRANT SELECT, INSERT, UPDATE ON TABLE public.api_daily_usage TO g2b_runtime;

-- Merge legacy recipient rows that differ only by whitespace or case. Delivery
-- and digest-window references move to a deterministic survivor before the
-- duplicate recipient is removed.
ALTER TABLE public.recipients NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.deliveries NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.digest_window_recipients NO FORCE ROW LEVEL SECURITY;

ALTER TABLE public.deliveries
    DROP CONSTRAINT IF EXISTS deliveries_digest_window_recipient_fk;

CREATE TEMP TABLE recipient_merge (
    tenant_id uuid NOT NULL,
    old_id uuid PRIMARY KEY,
    keep_id uuid NOT NULL
) ON COMMIT DROP;

INSERT INTO recipient_merge (tenant_id, old_id, keep_id)
WITH ranked AS (
    SELECT id, tenant_id,
           first_value(id) OVER (
               PARTITION BY tenant_id, lower(btrim(email))
               ORDER BY created_at, id
           ) AS keep_id,
           row_number() OVER (
               PARTITION BY tenant_id, lower(btrim(email))
               ORDER BY created_at, id
           ) AS position
    FROM public.recipients
)
SELECT tenant_id, id, keep_id FROM ranked WHERE position > 1;

-- A legacy case-duplicate could have produced two deliveries for the same
-- recipient/window. Keep the most complete record before moving the FK.
WITH ranked_deliveries AS (
    SELECT d.id,
           row_number() OVER (
               PARTITION BY d.tenant_id, d.schedule_id,
                            coalesce(m.keep_id, d.recipient_id), d.due_at
               ORDER BY CASE d.status
                            WHEN 'sent' THEN 0
                            WHEN 'sending' THEN 1
                            WHEN 'pending' THEN 2
                            ELSE 3
                        END,
                        d.attempts DESC, d.created_at, d.id
           ) AS position
    FROM public.deliveries d
    LEFT JOIN recipient_merge m
      ON m.tenant_id = d.tenant_id AND m.old_id = d.recipient_id
)
DELETE FROM public.deliveries d
USING ranked_deliveries ranked
WHERE d.id = ranked.id AND ranked.position > 1;

UPDATE public.deliveries d
SET recipient_id = merge.keep_id
FROM recipient_merge merge
WHERE d.tenant_id = merge.tenant_id AND d.recipient_id = merge.old_id;

WITH ranked_snapshots AS (
    SELECT snapshot.id,
           row_number() OVER (
               PARTITION BY snapshot.tenant_id, snapshot.schedule_id,
                            snapshot.due_at, snapshot.window_end_at,
                            coalesce(merge.keep_id, snapshot.recipient_id)
               ORDER BY snapshot.id
           ) AS position
    FROM public.digest_window_recipients snapshot
    LEFT JOIN recipient_merge merge
      ON merge.tenant_id = snapshot.tenant_id
     AND merge.old_id = snapshot.recipient_id
)
DELETE FROM public.digest_window_recipients snapshot
USING ranked_snapshots ranked
WHERE snapshot.id = ranked.id AND ranked.position > 1;

UPDATE public.digest_window_recipients snapshot
SET recipient_id = merge.keep_id
FROM recipient_merge merge
WHERE snapshot.tenant_id = merge.tenant_id
  AND snapshot.recipient_id = merge.old_id;

DELETE FROM public.recipients recipient
USING recipient_merge merge
WHERE recipient.tenant_id = merge.tenant_id AND recipient.id = merge.old_id;

UPDATE public.recipients SET email = lower(btrim(email));
UPDATE public.digest_window_recipients snapshot
SET email = recipient.email
FROM public.recipients recipient
WHERE recipient.tenant_id = snapshot.tenant_id
  AND recipient.id = snapshot.recipient_id;

ALTER TABLE public.recipients
    ADD CONSTRAINT recipients_email_normalized
    CHECK (email = lower(btrim(email)) AND length(email) > 0);
CREATE UNIQUE INDEX recipients_tenant_lower_email_unique
    ON public.recipients (tenant_id, lower(email));

ALTER TABLE public.deliveries
    ADD CONSTRAINT deliveries_digest_window_recipient_fk
    FOREIGN KEY (tenant_id, schedule_id, due_at, window_end_at, recipient_id)
    REFERENCES public.digest_window_recipients
        (tenant_id, schedule_id, due_at, window_end_at, recipient_id);

ALTER TABLE public.recipients FORCE ROW LEVEL SECURITY;
ALTER TABLE public.deliveries FORCE ROW LEVEL SECURITY;
ALTER TABLE public.digest_window_recipients FORCE ROW LEVEL SECURITY;
