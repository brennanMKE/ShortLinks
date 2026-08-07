-- campaigns: a user-owned grouping entity for short links that promote the
-- same thing (see docs/campaigns-plan.md). Campaign membership on links is
-- added in a later migration (#0099) — this migration only creates the
-- entity itself.
--
-- The five default_utm_* columns are a decided addition to the plan's schema
-- (#0098): selecting a campaign in the create form prefills the UTM builder
-- fields, and every prefilled value stays editable. default_utm_campaign is
-- populated from the slug at create time by internal/campaigns.Store.Create;
-- nothing else consumes these columns yet.
CREATE TABLE campaigns (
    id                    BIGSERIAL PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id),
    name                  TEXT NOT NULL,
    slug                  TEXT NOT NULL,
    description           TEXT,
    starts_at             TIMESTAMPTZ,
    ends_at               TIMESTAMPTZ,
    archived              BOOLEAN NOT NULL DEFAULT FALSE,
    default_utm_source    TEXT,
    default_utm_medium    TEXT,
    default_utm_campaign  TEXT,
    default_utm_term      TEXT,
    default_utm_content   TEXT,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- Campaigns are scoped per user, matching how links is owned. Slug
    -- uniqueness is per user, not global.
    UNIQUE (user_id, slug),

    -- #0102 uses [starts_at, ends_at] as the default chart window; an
    -- inverted range would silently render an always-empty chart. The
    -- handler already rejects this with a 400, but the constraint is the
    -- backstop against any other write path (direct DB access, a future
    -- store caller that skips the handler). Either bound may be NULL.
    CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at >= starts_at)
);

-- List / scope campaigns by owner.
CREATE INDEX idx_campaigns_user_id ON campaigns (user_id);
