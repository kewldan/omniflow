package adminauthpg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
	"github.com/omniflow/omniflow/internal/adminauth"
	"github.com/omniflow/omniflow/internal/database/dbgen"
)

// Passkeys for operators.
//
// A passkey signs in on its own. The authenticator proves possession of a
// private key and verifies the person holding it, so an assertion carries both
// factors and starts a complete session rather than a pending one — the same
// shape as an OIDC sign-in, and for the same reason.
//
// The password and its second factor are untouched and remain the way back in
// when every key is lost. That is deliberate: it means revoking a passkey is
// never an account-recovery event, and it means this table can be emptied
// without locking anybody out.

// Passkey is one registered credential as the panel sees it.
//
// There is no public key here and no credential identifier. Neither is secret,
// but neither is any use to an operator reading a list, and a response that
// carries only what a screen renders cannot leak what it does not hold.
type Passkey struct {
	ID           string    `json:"id"`
	Label        string    `json:"label"`
	CreatedAt    time.Time `json:"createdAt"`
	LastUsedAt   time.Time `json:"lastUsedAt,omitzero"`
	Discoverable bool      `json:"discoverable"`
}

// PasskeyChallenge is what the browser needs, and what must come back.
//
// State is the WebAuthn session — the challenge and what was asked of the
// authenticator — which the caller keeps out of band between the two steps. It
// is opaque here and sealed by the transport, so a browser cannot choose its
// own challenge.
type PasskeyChallenge struct {
	Options any    `json:"options"`
	State   []byte `json:"-"`
}

// ErrPasskeysUnavailable reports an installation with no public URL, which
// leaves nothing to bind a credential to.
var ErrPasskeysUnavailable = errors.New("passkeys need APP_PUBLIC_URL")

// passkeyUser adapts an operator to what the WebAuthn library asks for.
//
// The identifier is the account's UUID bytes rather than its email: a user
// handle is what the authenticator stores and returns, and an email is a value
// people change.
type passkeyUser struct {
	id          []byte
	name        string
	displayName string
	credentials []webauthn.Credential
}

func (user passkeyUser) WebAuthnID() []byte                         { return user.id }
func (user passkeyUser) WebAuthnName() string                       { return user.name }
func (user passkeyUser) WebAuthnDisplayName() string                { return user.displayName }
func (user passkeyUser) WebAuthnCredentials() []webauthn.Credential { return user.credentials }

// PasskeysEnabled reports whether the installation can offer them at all.
func (service *Service) PasskeysEnabled() bool { return service.webauthn != nil }

// Passkeys lists what an operator has registered.
func (service *Service) Passkeys(ctx context.Context, adminUserID string) ([]Passkey, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(service.pool).ListAdminPasskeys(ctx, id)
	if err != nil {
		return nil, err
	}
	keys := make([]Passkey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, Passkey{
			ID: uuidString(row.ID), Label: row.Label,
			CreatedAt: row.CreatedAt.Time, LastUsedAt: row.LastUsedAt.Time,
			Discoverable: row.Discoverable,
		})
	}
	return keys, nil
}

// BeginPasskeyRegistration asks the browser to create a credential.
func (service *Service) BeginPasskeyRegistration(
	ctx context.Context, adminUserID string,
) (PasskeyChallenge, error) {
	if !service.PasskeysEnabled() {
		return PasskeyChallenge{}, ErrPasskeysUnavailable
	}
	user, err := service.passkeyUser(ctx, adminUserID)
	if err != nil {
		return PasskeyChallenge{}, err
	}

	creation, session, err := service.webauthn.BeginRegistration(user,
		// Discoverable, because the whole point is signing in without first
		// naming the account. A non-discoverable credential would still work as
		// a second factor and would silently fail to appear on the sign-in
		// screen, which is the confusing half of both worlds.
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		// Verification required: an assertion the authenticator did not verify
		// a person for is possession alone, and this product signs in with one
		// factor only because that factor is two.
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		// Excluding what is already registered stops an authenticator quietly
		// replacing its own credential, which would look like a second key that
		// never appears.
		webauthn.WithExclusions(credentialDescriptors(user.credentials)),
	)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	encoded, err := encodeSession(session)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	return PasskeyChallenge{Options: creation.Response, State: encoded}, nil
}

// FinishPasskeyRegistration stores the credential the browser produced.
func (service *Service) FinishPasskeyRegistration(
	ctx context.Context, adminUserID, label string, state []byte,
	response *protocol.ParsedCredentialCreationData, request RequestContext,
) (Passkey, error) {
	if !service.PasskeysEnabled() {
		return Passkey{}, ErrPasskeysUnavailable
	}
	session, err := decodeSession(state)
	if err != nil {
		return Passkey{}, ErrInvalidCredentials
	}
	user, err := service.passkeyUser(ctx, adminUserID)
	if err != nil {
		return Passkey{}, err
	}

	credential, err := service.webauthn.CreateCredential(user, session, response)
	if err != nil {
		return Passkey{}, ErrInvalidCredentials
	}
	// The library verifies the attestation and the challenge; it does not
	// decide what this product requires of the result. An unverified credential
	// is refused here rather than stored and refused at every sign-in.
	if !credential.Flags.UserVerified {
		return Passkey{}, adminauth.ErrPasskeyUnverified
	}

	owner, err := parseUUID(adminUserID)
	if err != nil {
		return Passkey{}, err
	}
	var stored Passkey
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.CreateAdminPasskey(ctx, dbgen.CreateAdminPasskeyParams{
			AdminUserID:  owner,
			CredentialID: credential.ID,
			PublicKey:    credential.PublicKey,
			Label:        adminauth.PasskeyLabel(label, ""),
			SignCount:    int64(credential.Authenticator.SignCount),
			Aaguid:       credential.Authenticator.AAGUID,
			// True by construction, not by observation: registration asked for
			// a resident key as a requirement, so an authenticator that could
			// not store one failed the ceremony rather than reaching here.
			Discoverable: true,
			UserVerified: credential.Flags.UserVerified,
			CreatedIp:    request.IP,
		})
		if txErr != nil {
			return txErr
		}
		stored = Passkey{
			ID: uuidString(row.ID), Label: row.Label,
			CreatedAt: row.CreatedAt.Time, Discoverable: row.Discoverable,
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.passkey.registered", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"passkeyId": stored.ID, "label": stored.Label},
		})
	})
	if err != nil {
		return Passkey{}, err
	}
	// Registering a way into the account is exactly the act somebody with a
	// stolen session would perform, so it is announced like the others.
	if err = service.notifyPasskeySecurity(ctx, adminUserID, "admin.passkey.registered"); err != nil {
		return Passkey{}, err
	}
	return stored, nil
}

// BeginPasskeyLogin asks the browser for any credential this site owns.
//
// It names no account: a discoverable credential carries its own user handle,
// which is what lets an operator sign in without typing who they are.
func (service *Service) BeginPasskeyLogin() (PasskeyChallenge, error) {
	if !service.PasskeysEnabled() {
		return PasskeyChallenge{}, ErrPasskeysUnavailable
	}
	assertion, session, err := service.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	encoded, err := encodeSession(session)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	return PasskeyChallenge{Options: assertion.Response, State: encoded}, nil
}

// FinishPasskeyLogin verifies an assertion and issues a complete session.
func (service *Service) FinishPasskeyLogin(
	ctx context.Context, state []byte,
	response *protocol.ParsedCredentialAssertionData, request RequestContext,
) (LoginResult, error) {
	if !service.PasskeysEnabled() {
		return LoginResult{}, ErrPasskeysUnavailable
	}
	session, err := decodeSession(state)
	if err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}

	queries := dbgen.New(service.pool)
	var (
		storedRow dbgen.AdminPasskey
		account   dbgen.AdminUser
	)
	// The handler is how the library asks "whose credential is this?". It runs
	// before any signature is checked, so it may only look things up.
	lookup := func(rawID, _ []byte) (webauthn.User, error) {
		row, lookupErr := queries.GetAdminPasskeyByCredential(ctx, rawID)
		if lookupErr != nil {
			return nil, ErrInvalidCredentials
		}
		owner, lookupErr := queries.GetAdminUser(ctx, row.AdminUserID)
		if lookupErr != nil {
			return nil, ErrInvalidCredentials
		}
		storedRow, account = row, owner
		return passkeyUser{
			id: owner.ID.Bytes[:], name: owner.Email, displayName: owner.DisplayName,
			credentials: []webauthn.Credential{credentialFrom(row)},
		}, nil
	}

	credential, err := service.webauthn.ValidateDiscoverableLogin(lookup, session, response)
	if err != nil {
		service.auditPasskeyFailure(ctx, request, "assertion_rejected")
		return LoginResult{}, ErrInvalidCredentials
	}
	if !credential.Flags.UserVerified {
		service.auditPasskeyFailure(ctx, request, "not_user_verified")
		return LoginResult{}, adminauth.ErrPasskeyUnverified
	}
	// A counter that did not advance means two authenticators are answering for
	// one credential. The sign-in is refused and the account is told, because
	// the person holding the original has no other way to learn of the copy.
	if err = adminauth.CheckSignCount(
		uint32(storedRow.SignCount), credential.Authenticator.SignCount,
	); err != nil {
		service.auditPasskeyFailure(ctx, request, "sign_count_regressed")
		if notifyErr := service.notifyPasskeySecurity(
			ctx, uuidString(account.ID), "admin.passkey.cloned",
		); notifyErr != nil {
			return LoginResult{}, notifyErr
		}
		return LoginResult{}, err
	}
	if account.Status != "active" {
		return LoginResult{}, ErrAccountDisabled
	}

	if err = queries.RecordAdminPasskeyUse(ctx, dbgen.RecordAdminPasskeyUseParams{
		PasskeyID:  storedRow.ID,
		SignCount:  int64(credential.Authenticator.SignCount),
		LastUsedIp: request.IP,
	}); err != nil {
		return LoginResult{}, err
	}
	return service.startPasskeySession(ctx, account, request)
}

// RenamePasskey changes what an operator calls one of their own keys.
func (service *Service) RenamePasskey(
	ctx context.Context, adminUserID, passkeyID, label string, request RequestContext,
) (Passkey, error) {
	owner, err := parseUUID(adminUserID)
	if err != nil {
		return Passkey{}, err
	}
	key, err := parseUUID(passkeyID)
	if err != nil {
		return Passkey{}, err
	}
	var renamed Passkey
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.RenameAdminPasskey(ctx, dbgen.RenameAdminPasskeyParams{
			PasskeyID: key, AdminUserID: owner, Label: adminauth.PasskeyLabel(label, ""),
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		renamed = Passkey{
			ID: uuidString(row.ID), Label: row.Label,
			CreatedAt: row.CreatedAt.Time, LastUsedAt: row.LastUsedAt.Time,
			Discoverable: row.Discoverable,
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.passkey.renamed", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"passkeyId": renamed.ID, "label": renamed.Label},
		})
	})
	return renamed, err
}

// DeletePasskey revokes one of an operator's own keys.
func (service *Service) DeletePasskey(
	ctx context.Context, adminUserID, passkeyID string, request RequestContext,
) error {
	owner, err := parseUUID(adminUserID)
	if err != nil {
		return err
	}
	key, err := parseUUID(passkeyID)
	if err != nil {
		return err
	}
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		row, txErr := queries.DeleteAdminPasskey(ctx, dbgen.DeleteAdminPasskeyParams{
			PasskeyID: key, AdminUserID: owner,
		})
		if errors.Is(txErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if txErr != nil {
			return txErr
		}
		return appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: adminUserID,
			Action: "admin.passkey.revoked", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: adminUserID, RequestID: request.RequestID,
			Metadata: map[string]any{"passkeyId": uuidString(row.ID), "label": row.Label},
		})
	})
	if err != nil {
		return err
	}
	// Revocation is announced for the same reason registration is: it is what
	// somebody removing a person's way back in would do.
	return service.notifyPasskeySecurity(ctx, adminUserID, "admin.passkey.revoked")
}

func (service *Service) passkeyUser(ctx context.Context, adminUserID string) (passkeyUser, error) {
	id, err := parseUUID(adminUserID)
	if err != nil {
		return passkeyUser{}, err
	}
	queries := dbgen.New(service.pool)
	account, err := queries.GetAdminUser(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return passkeyUser{}, ErrNotFound
	}
	if err != nil {
		return passkeyUser{}, err
	}
	rows, err := queries.ListAdminPasskeys(ctx, id)
	if err != nil {
		return passkeyUser{}, err
	}
	credentials := make([]webauthn.Credential, 0, len(rows))
	for _, row := range rows {
		credentials = append(credentials, credentialFrom(row))
	}
	return passkeyUser{
		id: account.ID.Bytes[:], name: account.Email,
		displayName: account.DisplayName, credentials: credentials,
	}, nil
}

func credentialFrom(row dbgen.AdminPasskey) webauthn.Credential {
	return webauthn.Credential{
		ID:        row.CredentialID,
		PublicKey: row.PublicKey,
		Flags:     webauthn.CredentialFlags{UserVerified: row.UserVerified},
		Authenticator: webauthn.Authenticator{
			AAGUID: row.Aaguid, SignCount: uint32(row.SignCount),
		},
	}
}

func credentialDescriptors(credentials []webauthn.Credential) []protocol.CredentialDescriptor {
	descriptors := make([]protocol.CredentialDescriptor, 0, len(credentials))
	for _, credential := range credentials {
		descriptors = append(descriptors, credential.Descriptor())
	}
	return descriptors
}

// encodeSession and decodeSession move the WebAuthn session between the two
// steps of an exchange.
//
// It holds the challenge and what was demanded of the authenticator, and it
// must survive the round trip without the browser being able to choose it. The
// transport seals it into a short-lived cookie — the same treatment the OIDC
// flow state gets — so this pair only has to be a stable encoding.
func encodeSession(session *webauthn.SessionData) ([]byte, error) {
	return json.Marshal(session)
}

func decodeSession(state []byte) (webauthn.SessionData, error) {
	var session webauthn.SessionData
	if err := json.Unmarshal(state, &session); err != nil {
		return webauthn.SessionData{}, err
	}
	if len(session.Challenge) == 0 {
		return webauthn.SessionData{}, errors.New("passkey session carries no challenge")
	}
	return session, nil
}

// startPasskeySession issues a complete session for a verified assertion.
//
// It mirrors the OIDC path rather than the password one: no pending challenge,
// because the authenticator already proved possession and verified the person,
// and a second factor demanded after that would be a third.
func (service *Service) startPasskeySession(
	ctx context.Context, row dbgen.AdminUser, request RequestContext,
) (LoginResult, error) {
	token, digest, err := adminauth.NewSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	csrfSecret, err := adminauth.NewCSRFSecret()
	if err != nil {
		return LoginResult{}, err
	}
	now := service.now()
	idle, absolute := service.sessions.SessionDeadlines(now, false)

	var result LoginResult
	err = service.inTx(ctx, func(queries *dbgen.Queries) error {
		fresh, txErr := queries.RecordAdminLoginSuccess(ctx, row.ID)
		if txErr != nil {
			return txErr
		}
		session, txErr := queries.CreateAdminSession(ctx, dbgen.CreateAdminSessionParams{
			AdminUserID: row.ID, TokenHash: digest, CsrfSecret: csrfSecret,
			PendingTotp: false, AuthMethods: []string{"passkey"},
			Ip: request.IP, UserAgent: optionalText(truncate(request.UserAgent, 400)),
			IdleExpiresAt: timestamp(idle), AbsoluteExpiresAt: timestamp(absolute),
		})
		if txErr != nil {
			return txErr
		}
		roles, txErr := loadRoles(ctx, queries, row.ID)
		if txErr != nil {
			return txErr
		}
		if txErr = appendAudit(ctx, queries, AuditEntry{
			ActorType: "admin", ActorID: uuidString(row.ID),
			Action: "admin.login", Category: "authentication", Outcome: "success",
			TargetType: "admin_user", TargetID: uuidString(row.ID), RequestID: request.RequestID,
			Metadata: map[string]any{"sessionId": uuidString(session.ID), "method": "passkey"},
		}); txErr != nil {
			return txErr
		}
		result = LoginResult{
			Token:     token,
			CSRFToken: adminauth.CSRFToken(csrfSecret),
			ExpiresAt: idle,
			Account:   service.accountFrom(fresh, roles),
		}
		return nil
	})
	if err != nil {
		return LoginResult{}, err
	}
	return result, nil
}

// notifyPasskeySecurity tells the account that its ways in changed.
//
// Registering and revoking a key are both announced, because both are what
// somebody who had stolen a session would do, and the person holding the
// account has no other way to learn of either.
func (service *Service) notifyPasskeySecurity(ctx context.Context, adminUserID, event string) error {
	return service.inTx(ctx, func(queries *dbgen.Queries) error {
		// The dedupe key includes the moment, so two legitimate registrations
		// produce two notices rather than the second being swallowed as a
		// repeat of the first.
		dedupe := adminUserID + ":" + service.now().UTC().Format(time.RFC3339Nano)
		return notifySecurity(ctx, queries, event, adminUserID, dedupe, "passkey")
	})
}

// auditPasskeyFailure records a refused assertion.
//
// A refusal is recorded without naming an account: the assertion did not prove
// who it came from, so attributing it would be a guess written into the trail.
// The reason is a category rather than the library's message, which can echo
// the payload back.
func (service *Service) auditPasskeyFailure(ctx context.Context, request RequestContext, reason string) {
	queries := dbgen.New(service.pool)
	if err := appendAudit(ctx, queries, AuditEntry{
		ActorType: "anonymous",
		Action:    "admin.login", Category: "authentication", Outcome: "failure",
		TargetType: "admin_user", RequestID: request.RequestID,
		Metadata: map[string]any{"method": "passkey", "reason": reason},
	}); err != nil {
		// The sign-in has already failed and the caller is about to say so.
		// Losing the trail entry must not change the answer they get.
		_ = err
	}
}
