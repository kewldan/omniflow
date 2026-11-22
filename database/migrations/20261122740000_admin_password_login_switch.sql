-- An operator may turn password sign-in off for their own account once a
-- passkey of theirs has signed in, so a stolen password alone opens nothing.
-- password_login_disabled_at is when they did; NULL keeps passwords working.
-- password_login_allowed_until is the server-side escape hatch: the api's
-- `admin password-login` command reopens password sign-in until then, for an
-- operator whose passkey is gone, without turning the switch off for good.
ALTER TABLE admin_users ADD COLUMN password_login_disabled_at timestamptz;
ALTER TABLE admin_users ADD COLUMN password_login_allowed_until timestamptz;
