-- Where a customer gets a recommended application.
--
-- A client already carries one optional download address. That is not enough
-- for an installation whose customers cannot reach the vendor: from Russia
-- without a VPN, GitHub and a vendor's own site often do not open, which is the
-- moment somebody is trying to install the VPN client in the first place.
--
-- Each row is either a link to a store page (App Store, Google Play, the
-- vendor's page) or a mirror: a GitHub release asset the worker copies into the
-- installation's object store and keeps at the newest release, served by the
-- API from the installation's own address.

CREATE TABLE connect_downloads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    client_id uuid NOT NULL,
    kind text NOT NULL,
    label text NOT NULL,
    -- A store link.
    url text,
    -- A mirror: the repository, the prefix a release's tag must start with
    -- (INCY tags Android and desktop builds apart), and the asset's name, which
    -- may carry * and ? wildcards.
    source_repo text,
    tag_prefix text DEFAULT '' NOT NULL,
    asset_pattern text,
    -- The file the mirror holds now. All of these are set together, by the
    -- worker, once a release asset has been copied and its digest checked.
    version text,
    file_name text,
    size_bytes bigint,
    checksum text,
    content_type text,
    fetched_at timestamp with time zone,
    -- The worker checks a mirror when this moment has passed. Saving a source
    -- or pressing "check now" sets it to now.
    next_check_at timestamp with time zone DEFAULT now() NOT NULL,
    checked_at timestamp with time zone,
    last_error text,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by uuid,
    CONSTRAINT connect_downloads_pkey PRIMARY KEY (id),
    CONSTRAINT connect_downloads_client_id_fkey FOREIGN KEY (client_id)
        REFERENCES connect_clients(id) ON DELETE CASCADE,
    CONSTRAINT connect_downloads_updated_by_fkey FOREIGN KEY (updated_by)
        REFERENCES admin_users(id),
    CONSTRAINT connect_downloads_kind_valid CHECK (kind IN ('store', 'mirror')),
    CONSTRAINT connect_downloads_label_valid CHECK (char_length(label) BETWEEN 1 AND 40),
    -- A store link is handed to somebody about to install software, so it is
    -- https only, like a client's own download address.
    CONSTRAINT connect_downloads_url_valid CHECK (
        url IS NULL OR (url ~ '^https://' AND char_length(url) BETWEEN 11 AND 300 AND url !~ '[[:space:]]')
    ),
    CONSTRAINT connect_downloads_repo_valid CHECK (
        source_repo IS NULL OR source_repo ~ '^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$'
    ),
    CONSTRAINT connect_downloads_tag_prefix_valid CHECK (tag_prefix ~ '^[A-Za-z0-9_.+-]{0,40}$'),
    CONSTRAINT connect_downloads_asset_valid CHECK (
        asset_pattern IS NULL OR asset_pattern ~ '^[A-Za-z0-9_.+*?-]{1,120}$'
    ),
    CONSTRAINT connect_downloads_kind_fields CHECK (
        (kind = 'store' AND url IS NOT NULL AND source_repo IS NULL AND asset_pattern IS NULL)
        OR (kind = 'mirror' AND url IS NULL AND source_repo IS NOT NULL AND asset_pattern IS NOT NULL)
    ),
    CONSTRAINT connect_downloads_file_complete CHECK (
        (checksum IS NULL) = (file_name IS NULL)
        AND (checksum IS NULL) = (size_bytes IS NULL)
        AND (checksum IS NULL) = (version IS NULL)
        AND (checksum IS NULL) = (content_type IS NULL)
        AND (checksum IS NULL) = (fetched_at IS NULL)
    ),
    CONSTRAINT connect_downloads_file_only_mirror CHECK (kind = 'mirror' OR checksum IS NULL),
    CONSTRAINT connect_downloads_checksum_valid CHECK (checksum IS NULL OR checksum ~ '^[0-9a-f]{64}$'),
    -- 300 MB: the largest desktop build in use is about 160 MB.
    CONSTRAINT connect_downloads_size_valid CHECK (size_bytes IS NULL OR size_bytes BETWEEN 1 AND 314572800),
    CONSTRAINT connect_downloads_file_name_valid CHECK (
        file_name IS NULL OR file_name ~ '^[A-Za-z0-9_.+-]{1,120}$'
    ),
    CONSTRAINT connect_downloads_version_valid CHECK (version IS NULL OR char_length(version) BETWEEN 1 AND 60),
    CONSTRAINT connect_downloads_error_valid CHECK (last_error IS NULL OR char_length(last_error) <= 500)
);

COMMENT ON TABLE connect_downloads IS 'Store links and mirrored installers for a recommended application. Mirrors are GitHub release assets the worker keeps in the object store at the newest release.';

CREATE INDEX connect_downloads_client_idx ON connect_downloads USING btree (client_id, sort_order, label);

CREATE INDEX connect_downloads_due_idx ON connect_downloads USING btree (next_check_at)
    WHERE kind = 'mirror' AND enabled;

CREATE INDEX connect_downloads_checksum_idx ON connect_downloads USING btree (checksum)
    WHERE checksum IS NOT NULL;
