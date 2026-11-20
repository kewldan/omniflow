-- A customer's own name for one of their devices ("Kitchen tablet").
--
-- Remnawave owns the device list; this is a label Omniflow keeps beside it,
-- keyed by the device handle — a digest of the hardware ID, never the ID
-- itself. A device that disappears from the panel simply stops matching, and
-- the label goes with its subscription.
CREATE TABLE device_aliases (
    subscription_id uuid NOT NULL REFERENCES subscriptions (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    device_handle text NOT NULL,
    alias text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (subscription_id, device_handle),
    CONSTRAINT device_aliases_handle_shape CHECK (device_handle ~ '^[A-Za-z0-9_-]{16,64}$'),
    CONSTRAINT device_aliases_alias_length CHECK (char_length(alias) BETWEEN 1 AND 40)
);
