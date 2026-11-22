-- Whether a passkey may be backed up (synced: iCloud Keychain, Google Password
-- Manager) and whether it is. The WebAuthn check refuses an assertion whose
-- backup-eligible flag differs from the one the credential was registered
-- with, so without it every synced passkey was refused at sign-in. NULL for a
-- passkey registered before this column: its first sign-in records the flag
-- the authenticator reports, and later ones are held to it.
ALTER TABLE admin_passkeys ADD COLUMN backup_eligible boolean;
ALTER TABLE admin_passkeys ADD COLUMN backup_state boolean;
