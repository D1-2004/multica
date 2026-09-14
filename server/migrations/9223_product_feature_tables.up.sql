-- Global product feature identities and their immutable public release history.
-- Relationships are application-enforced; this repository does not use
-- database foreign keys or cascading actions.

CREATE TABLE IF NOT EXISTS product_feature (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    slug TEXT NOT NULL CHECK (slug = LOWER(slug) AND slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS product_feature_release (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    feature_id UUID NOT NULL,
    release_type TEXT NOT NULL CHECK (release_type IN ('new', 'improvement')),
    title TEXT NOT NULL CHECK (BTRIM(title) <> ''),
    description TEXT NOT NULL CHECK (BTRIM(description) <> ''),
    use_cases TEXT NOT NULL CHECK (BTRIM(use_cases) <> ''),
    usage_guide TEXT NOT NULL CHECK (BTRIM(usage_guide) <> ''),
    version_label TEXT NOT NULL DEFAULT '',
    image_requirement TEXT NOT NULL DEFAULT 'none'
        CHECK (image_requirement IN ('none', 'latest_at_publish', 'min_version')),
    required_image_version TEXT,
    previous_release_id UUID,
    published_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (
        (image_requirement = 'none' AND required_image_version IS NULL)
        OR
        (image_requirement <> 'none' AND NULLIF(BTRIM(required_image_version), '') IS NOT NULL)
    )
);
