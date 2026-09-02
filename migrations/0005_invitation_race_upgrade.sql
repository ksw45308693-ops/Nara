-- Upgrade installations that applied an earlier 0003 before invitation
-- mutations were serialized by normalized email. The migration administrator
-- must be a PostgreSQL superuser so it can replace functions owned by the
-- NOLOGIN namo_onboarding_definer role without granting that role membership.
-- Every invitation mutation acquires advisory locks in one global order:
-- tenant first, then normalized email. Never reverse this order; acceptance
-- and initial-administrator replacement rely on it to avoid deadlocks.

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
    IF EXISTS (SELECT 1 FROM public.users WHERE lower(email) = v_invitee_email) THEN
        RAISE EXCEPTION 'email already belongs to an account' USING ERRCODE = '23505';
    END IF;
    IF EXISTS (SELECT 1 FROM public.users WHERE tenant_id = v_tenant_id) THEN
        RAISE EXCEPTION 'tenant is already onboarded' USING ERRCODE = '23505';
    END IF;

    INSERT INTO public.schedules (tenant_id, name, hour, minute, timezone, weekdays)
    VALUES (v_tenant_id, '기본 알림', 7, 0, 'Asia/Seoul', ARRAY[0,1,2,3,4,5,6]::smallint[])
    ON CONFLICT (tenant_id, name) DO NOTHING;

    -- An un-onboarded tenant has exactly one usable initial administrator
    -- bearer. Keep ordinary member invitations and this replacement email.
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

CREATE OR REPLACE FUNCTION public.onboarding_accept_invitation(
    p_token_hash text,
    p_display_name text,
    p_password_hash text
)
RETURNS TABLE (user_id uuid, tenant_id uuid, email text, role text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_invitation public.invitations%ROWTYPE;
    v_user_id uuid;
    v_tenant_id uuid;
    v_email text;
    v_display_name text := btrim(p_display_name);
BEGIN
    IF p_token_hash !~ '^[0-9a-f]{64}$'
       OR coalesce(octet_length(v_display_name), 0) = 0 OR octet_length(v_display_name) > 512
       OR coalesce(length(p_password_hash), 0) < 59 OR left(p_password_hash, 2) <> '$2' THEN
        RAISE EXCEPTION 'invalid invitation acceptance' USING ERRCODE = '22023';
    END IF;

    SELECT i.tenant_id, lower(i.email) INTO v_tenant_id, v_email
    FROM public.invitations i
    WHERE i.token_hash = p_token_hash;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invitation is unavailable' USING ERRCODE = 'P0002';
    END IF;

    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended('namo-tenant-onboarding:' || v_tenant_id::text, 0)
    );
    PERFORM pg_catalog.pg_advisory_xact_lock(
        pg_catalog.hashtextextended('namo-invitation:' || v_email, 0)
    );

    SELECT i.* INTO v_invitation
    FROM public.invitations i
    WHERE i.token_hash = p_token_hash
      AND i.accepted_at IS NULL
      AND i.expires_at > clock_timestamp()
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invitation is unavailable' USING ERRCODE = 'P0002';
    END IF;
    IF EXISTS (SELECT 1 FROM public.users WHERE lower(email) = v_email) THEN
        RAISE EXCEPTION 'email already belongs to an account' USING ERRCODE = '23505';
    END IF;

    INSERT INTO public.users (tenant_id, email, display_name, password_hash, role)
    VALUES (v_invitation.tenant_id, v_invitation.email, v_display_name, p_password_hash, v_invitation.role)
    RETURNING id INTO v_user_id;

    UPDATE public.invitations SET accepted_at = clock_timestamp()
    WHERE id = v_invitation.id AND accepted_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invitation is unavailable' USING ERRCODE = 'P0002';
    END IF;

    RETURN QUERY SELECT v_user_id, v_invitation.tenant_id, v_invitation.email, v_invitation.role;
END;
$$;

-- CREATE OR REPLACE preserves existing ACLs, but restore the intended owner
-- and privilege boundary explicitly in case a legacy installation drifted.
ALTER FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer;
ALTER FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) OWNER TO namo_onboarding_definer;
ALTER FUNCTION public.onboarding_accept_invitation(text, text, text) OWNER TO namo_onboarding_definer;

REVOKE ALL ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.onboarding_accept_invitation(text, text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.onboarding_create_tenant(uuid, text, text, text, text, text, timestamptz) TO namo_runtime;
GRANT EXECUTE ON FUNCTION public.onboarding_invite_member(uuid, uuid, text, text, text, text, timestamptz) TO namo_runtime;
GRANT EXECUTE ON FUNCTION public.onboarding_accept_invitation(text, text, text) TO namo_runtime;
