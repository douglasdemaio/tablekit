package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"tablekit.de/tablekit/internal/db/sqlcgen"
)

type webAuthnUser struct {
	id    uuid.UUID
	email string
	name  string
	creds []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte   { return u.id[:] }
func (u *webAuthnUser) WebAuthnName() string { return u.email }
func (u *webAuthnUser) WebAuthnDisplayName() string {
	if u.name != "" {
		return u.name
	}
	return u.email
}
func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func loadWebAuthnUser(ctx context.Context, q *sqlcgen.Queries, staff sqlcgen.StaffUser) (*webAuthnUser, error) {
	rows, err := q.ListPasskeyCredentialsForStaff(ctx, staff.ID)
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(rows))
	for _, row := range rows {
		var cred webauthn.Credential
		if err := json.Unmarshal(row.Credential, &cred); err != nil {
			continue
		}
		creds = append(creds, cred)
	}
	return &webAuthnUser{id: staff.ID, email: staff.Email, name: staff.Name, creds: creds}, nil
}

type PasskeyBegin struct {
	ChallengeID uuid.UUID
	Options     any
}

func (s *Service) BeginPasskeyLogin(ctx context.Context, merchantID uuid.UUID, email string) (PasskeyBegin, error) {
	var out PasskeyBegin
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByEmail(ctx, strings.TrimSpace(email))
		if err != nil {
			return ErrPasskeyUnavailable
		}
		user, err := loadWebAuthnUser(ctx, q, staff)
		if err != nil {
			return err
		}
		if len(user.creds) == 0 {
			return ErrNoPasskeys
		}
		options, sessionData, err := s.WA.BeginLogin(user)
		if err != nil {
			return ErrPasskeyUnavailable
		}
		data, err := json.Marshal(sessionData)
		if err != nil {
			return err
		}
		ch, err := q.CreateWebAuthnChallenge(ctx, sqlcgen.CreateWebAuthnChallengeParams{
			Column1: merchantID,
			Column2: staff.ID,
			Column3: "login",
			Column4: data,
			Column5: pgTime(s.Now().Add(s.Config.MFAPendingTTL)),
		})
		if err != nil {
			return err
		}
		out = PasskeyBegin{ChallengeID: ch.ID, Options: options}
		return nil
	})
	return out, err
}

func (s *Service) FinishPasskeyLogin(ctx context.Context, merchantID uuid.UUID, challengeID uuid.UUID, credential json.RawMessage, r *http.Request) (LoginResult, error) {
	var out LoginResult
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		ch, err := q.GetWebAuthnChallenge(ctx, sqlcgen.GetWebAuthnChallengeParams{
			Column1: challengeID,
			Column2: "login",
		})
		if err != nil {
			return ErrChallengeExpired
		}
		defer func() { _ = q.DeleteWebAuthnChallenge(ctx, challengeID) }()

		staff, err := q.GetStaffByID(ctx, ch.StaffID)
		if err != nil {
			return ErrPasskeyUnavailable
		}
		user, err := loadWebAuthnUser(ctx, q, staff)
		if err != nil {
			return err
		}
		var sessionData webauthn.SessionData
		if err := json.Unmarshal(ch.Data, &sessionData); err != nil {
			return ErrChallengeExpired
		}
		req := cloneWithJSONBody(r, credential)
		cred, err := s.WA.FinishLogin(user, sessionData, req)
		if err != nil {
			return ErrPasskeyUnavailable
		}
		stored, err := q.GetPasskeyByCredentialID(ctx, cred.ID)
		if err != nil {
			return ErrPasskeyUnavailable
		}
		updated, err := json.Marshal(cred)
		if err != nil {
			return err
		}
		if err := q.UpdatePasskeyCredential(ctx, sqlcgen.UpdatePasskeyCredentialParams{
			Column1: updated,
			Column2: stored.ID,
		}); err != nil {
			return err
		}
		ns, err := s.createSession(ctx, q, merchantID, staff.ID, "verified", requestIP(r), r.UserAgent())
		if err != nil {
			return err
		}
		out = LoginResult{
			Token: ns.Token, CSRFToken: ns.CSRFToken, Session: ns.Session,
			Staff: staff, Next: "/dashboard",
		}
		return nil
	})
	return out, err
}

func (s *Service) BeginPasskeyRegistration(ctx context.Context, merchantID, staffID uuid.UUID) (PasskeyBegin, error) {
	var out PasskeyBegin
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		staff, err := q.GetStaffByID(ctx, staffID)
		if err != nil {
			return err
		}
		user, err := loadWebAuthnUser(ctx, q, staff)
		if err != nil {
			return err
		}
		options, sessionData, err := s.WA.BeginRegistration(user)
		if err != nil {
			return err
		}
		data, err := json.Marshal(sessionData)
		if err != nil {
			return err
		}
		ch, err := q.CreateWebAuthnChallenge(ctx, sqlcgen.CreateWebAuthnChallengeParams{
			Column1: merchantID,
			Column2: staff.ID,
			Column3: "register",
			Column4: data,
			Column5: pgTime(s.Now().Add(s.Config.MFAPendingTTL)),
		})
		if err != nil {
			return err
		}
		out = PasskeyBegin{ChallengeID: ch.ID, Options: options}
		return nil
	})
	return out, err
}

func (s *Service) FinishPasskeyRegistration(ctx context.Context, merchantID, staffID, challengeID uuid.UUID, credential json.RawMessage, name string, r *http.Request) error {
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		ch, err := q.GetWebAuthnChallenge(ctx, sqlcgen.GetWebAuthnChallengeParams{
			Column1: challengeID,
			Column2: "register",
		})
		if err != nil {
			return ErrChallengeExpired
		}
		if ch.StaffID != staffID {
			return ErrChallengeExpired
		}
		defer func() { _ = q.DeleteWebAuthnChallenge(ctx, challengeID) }()

		staff, err := q.GetStaffByID(ctx, staffID)
		if err != nil {
			return err
		}
		user, err := loadWebAuthnUser(ctx, q, staff)
		if err != nil {
			return err
		}
		var sessionData webauthn.SessionData
		if err := json.Unmarshal(ch.Data, &sessionData); err != nil {
			return ErrChallengeExpired
		}
		req := cloneWithJSONBody(r, credential)
		cred, err := s.WA.FinishRegistration(user, sessionData, req)
		if err != nil {
			return ErrPasskeyUnavailable
		}
		encoded, err := json.Marshal(cred)
		if err != nil {
			return err
		}
		name = strings.TrimSpace(name)
		if name == "" {
			name = "Passkey"
		}
		if len(name) > 80 {
			name = name[:80]
		}
		if _, err := q.CreatePasskey(ctx, sqlcgen.CreatePasskeyParams{
			Column1: merchantID,
			Column2: staffID,
			Column3: cred.ID,
			Column4: encoded,
			Column5: name,
		}); err != nil {
			return err
		}
		return nil
	})
}

func (s *Service) ListPasskeys(ctx context.Context, merchantID, staffID uuid.UUID) ([]sqlcgen.ListPasskeysForStaffRow, error) {
	var rows []sqlcgen.ListPasskeysForStaffRow
	err := s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		var err error
		rows, err = q.ListPasskeysForStaff(ctx, staffID)
		return err
	})
	return rows, err
}

func (s *Service) DeletePasskey(ctx context.Context, merchantID, staffID, passkeyID uuid.UUID) error {
	return s.Store.WithTenant(ctx, merchantID, func(q *sqlcgen.Queries) error {
		return q.DeletePasskey(ctx, sqlcgen.DeletePasskeyParams{Column1: passkeyID, Column2: staffID})
	})
}

func cloneWithJSONBody(r *http.Request, body []byte) *http.Request {
	req := r.Clone(r.Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header = r.Header.Clone()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("Content-Length")
	return req
}

func requestIP(r *http.Request) string {
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return strings.TrimSpace(strings.Split(ip, ",")[0])
	}
	return r.RemoteAddr
}
