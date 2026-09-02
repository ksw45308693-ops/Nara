-- Close expired pending invitations before issuing a new bearer for the same
-- normalized email. The current schema uses accepted_at as a general closed marker:
-- a non-NULL value means accepted, administratively replaced, or expired and
-- discarded. Lookup and acceptance already require accepted_at IS NULL and a
-- future expires_at, so this does not make an expired bearer usable again.
--
-- Keep the global advisory-lock order from 0005: tenant first, then email.
-- The expired-row update must run only after both locks are held.

CREATE OR REPLACE FUNCTION public.onboarding_create_tenant(
    p_actor_user_id uuid,
    p_tenant_name text,
    p_contact_email text,
    p_invitee_email text,
    p_display_name text,
    p_token_hash text,
    p_expires_at timestamptz
)
RETURNS TABLE (tenant_id uuid, invitation_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_tenant_id uuid;
    v_invitation_id uuid;
    v_contact_email text := lower(btrim(p_contact_email));
    v_invitee_email text := lower(btrim(p_invitee_email));
    v_tenant_name text := btrim(p_tenant_name);
    v_display_name text := btrim(p_display_name);
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM public.users
        WHERE id = p_actor_user_id AND tenant_id IS NULL AND role = 'platform_admin'
    ) THEN
        RAISE EXCEPTION 'platform administrator role is required' USING ERRCODE = '42501';
    END IF;
    IF coalesce(octet_length(v_tenant_name), 0) = 0 OR octet_length(v_tenant_name) > 512
       OR coalesce(octet_length(v_display_name), 0) = 0 OR octet_length(v_display_name) > 512 THEN
        RAISE EXCEPTION 'tenant and invitee names are required' USING ERRCODE = '22023';
    END IF;
    IF v_contact_email !~ '^[^[:space:]@]+@[^[:space:]@]+$'
       OR v_invitee_email !~ '^[^[:space:]@]+@[^[:space:]@]+$'
       OR octet_length(v_contact_email) > 320 OR octet_length(v_invitee_email) > 320 THEN
        RAISE EXCEPTION 'valid email addresses are required' USING ERRCODE = '22023';
    END IF;
    IF p_token_hash !~ '^[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'valid token hash is required' USING ERRCODE = '22023';
    END IF;
    IF p_expires_at IS NULL OR p_expires_at <= clock_timestamp()
       OR p_expires_at > clock_timestamp() + interval '48 hours 5 minutes' THEN
        RAISE EXCEPTION 'invitation expiry must be within 48 hours' USING ERRCODE = '22023';
    END IF;

    SELECT t.id INTO v_tenant_id
    FROM public.tenants t
    WHERE lower(btrim(t.name)) = lower(v_tenant_name)
      AND lower(btrim(t.contact_email)) = v_contact_email;

    IF v_tenant_id IS NULL THEN
        BEGIN
            INSERT INTO public.tenants (name, contact_email)
            VALUES (v_tenant_name, v_contact_email)
            RETURNING id INTO v_tenant_id;
        EXCEPTION WHEN unique_violation THEN
            SELECT t.id INTO v_tenant_id
            FROM public.tenants t
            WHERE lower(btrim(t.name)) = lower(v_tenant_name)
              AND lower(btrim(t.contact_email)) = v_contact_email;
        END;
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended('namo-tenant-onboarding:' || v_tenant_id::text, 0)
    );
    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended('namo-invitation:' || v_invitee_email, 0)
    );

    UPDATE public.invitations
    SET accepted_at = clock_timestamp()
    WHERE lower(email) = v_invitee_email
      AND accepted_at IS NULL
      AND expires_at <= clock_timestamp();

    IF EXISTS (SELECT 1 FROM public.users WHERE lower(email) = v_invitee_email) THEN
        RAISE EXCEPTION 'email already belongs to an account' USING ERRCODE = '23505';
    END IF;
    IF EXISTS (SELECT 1 FROM public.users WHERE tenant_id = v_tenant_id) THEN
        RAISE EXCEPTION 'tenant is already onboarded' USING ERRCODE = '23505';
    END IF;

    INSERT INTO public.schedules (tenant_id, name, hour, minute, timezone, weekdays)
    VALUES (v_tenant_id, '기본 알림', 7, 0, 'Asia/Seoul', ARRAY[0,1,2,3,4,5,6]::smallint[])
    ON CONFLICT (tenant_id, name) DO NOTHING;

    UPDATE public.invitations
    SET accepted_at = clock_timestamp()
    WHERE tenant_id = v_tenant_id
      AND role = 'tenant_admin' AND accepted_at IS NULL
      AND lower(email) <> v_invitee_email;

    INSERT INTO public.invitations (tenant_id, email, display_name, role, token_hash, expires_at)
    VALUES (v_tenant_id, v_invitee_email, v_display_name, 'tenant_admin', p_token_hash, p_expires_at)
    ON CONFLICT (tenant_id, (lower(email))) WHERE accepted_at IS NULL DO UPDATE SET
        display_name = EXCLUDED.display_name,
        role = EXCLUDED.role,
        token_hash = EXCLUDED.token_hash,
        expires_at = EXCLUDED.expires_at,
        accepted_at = NULL,
        created_at = clock_timestamp()
    RETURNING id INTO v_invitation_id;

    RETURN QUERY SELECT v_tenant_id, v_invitation_id;
END;
$$;

CREATE OR REPLACE FUNCTION public.onboarding_invite_member(
    p_actor_user_id uuid,
    p_tenant_id uuid,
    p_invitee_email text,
    p_display_name text,
    p_role text,
    p_token_hash text,
    p_expires_at timestamptz
)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_email text := lower(btrim(p_invitee_email));
    v_display_name text := btrim(p_display_name);
    v_invitation_id uuid;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM public.users
        WHERE id = p_actor_user_id AND tenant_id = p_tenant_id AND role = 'tenant_admin'
    ) THEN
        RAISE EXCEPTION 'tenant administrator role is required' USING ERRCODE = '42501';
    END IF;
    IF p_role NOT IN ('tenant_admin', 'member') THEN
        RAISE EXCEPTION 'invalid invitation role' USING ERRCODE = '22023';
    END IF;
    IF coalesce(octet_length(v_display_name), 0) = 0 OR octet_length(v_display_name) > 512
       OR v_email !~ '^[^[:space:]@]+@[^[:space:]@]+$' OR octet_length(v_email) > 320 THEN
        RAISE EXCEPTION 'valid invitee identity is required' USING ERRCODE = '22023';
    END IF;
    IF p_token_hash !~ '^[0-9a-f]{64}$' THEN
        RAISE EXCEPTION 'valid token hash is required' USING ERRCODE = '22023';
    END IF;
    IF p_expires_at IS NULL OR p_expires_at <= clock_timestamp()
       OR p_expires_at > clock_timestamp() + interval '48 hours 5 minutes' THEN
        RAISE EXCEPTION 'invitation expiry must be within 48 hours' USING ERRCODE = '22023';
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended('namo-tenant-onboarding:' || p_tenant_id::text, 0)
    );
    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended('namo-invitation:' || v_email, 0)
    );

    UPDATE public.invitations
    SET accepted_at = clock_timestamp()
    WHERE lower(email) = v_email
      AND accepted_at IS NULL
      AND expires_at <= clock_timestamp();

    IF EXISTS (SELECT 1 FROM public.users WHERE lower(email) = v_email) THEN
        RAISE EXCEPTION 'email already belongs to an account' USING ERRCODE = '23505';
    END IF;

    INSERT INTO public.invitations (tenant_id, email, display_name, role, token_hash, expires_at)
    VALUES (p_tenant_id, v_email, v_display_name, p_role, p_token_hash, p_expires_at)
    ON CONFLICT (tenant_id, (lower(email))) WHERE accepted_at IS NULL DO UPDATE SET
        display_name = EXCLUDED.display_name,
        role = EXCLUDED.role,
        token_hash = EXCLUDED.token_hash,
        expires_at = EXCLUDED.expires_at,
        accepted_at = NULL,
        created_at = clock_timestamp()
    RETURNING id INTO v_invitation_id;

    RETURN v_invitation_id;
END;
$$;

-- Restore the SECURITY DEFINER ownership and execute-only public boundary
-- explicitly after replacing functions on an already-migrated installation.
ALTER FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer;
ALTER FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer;

REVOKE ALL ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) TO namo_runtime;
GRANT EXECUTE ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) TO namo_runtime;
