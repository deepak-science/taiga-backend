package models

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/db"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
	"github.com/jackc/pgx/v5/pgtype"
)

// Signing in from a CLI: taiga's own device flow, in the shape of RFC 8628 (ROADMAP M14).
const (
	// DeviceCodeTTL is how long the user has to approve a sign-in.
	DeviceCodeTTL = 10 * time.Minute
	// DevicePollInterval is how often the CLI is told to ask whether they have.
	DevicePollInterval = 5 * time.Second
	// devicePollSlack lets a poll land a little early, so a client on the interval isn't told to slow down for network jitter.
	devicePollSlack = time.Second
	// CLISessionTTL is how long a session lasts unused; each refresh starts it again.
	CLISessionTTL = 90 * 24 * time.Hour
	// maxDeviceAuthorizations caps the sign-ins waiting at once: asking for one takes no credentials and writes a row.
	maxDeviceAuthorizations = 100
	// Letters that don't spell words and aren't mistaken for each other, the alphabet RFC 8628 suggests.
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLength   = 8
	maxClientName    = 64
)

// What polling for a device sign-in can answer, named as RFC 8628 names them.
var (
	ErrAuthorizationPending = errors.New("authorization_pending")
	ErrSlowDown             = errors.New("slow_down")
	ErrExpiredToken         = errors.New("expired_token")
	ErrAccessDenied         = errors.New("access_denied")
	// ErrInvalidGrant is a device code or refresh token that isn't known: never issued, already used, revoked or lapsed.
	ErrInvalidGrant = errors.New("invalid_grant")
	// ErrTooManyDevices means too many sign-ins are waiting for approval at once.
	ErrTooManyDevices = errors.New("too many sign-ins are waiting for approval; try again in a few minutes")
)

// DeviceStart is what a CLI gets for asking to sign in.
type DeviceStart struct {
	// DeviceCode is the CLI's secret, to poll with.
	DeviceCode string
	// UserCode is for the user to read to the browser, e.g. "WDJB-MJHT".
	UserCode  string
	ExpiresAt time.Time
}

// DeviceRequest is what the approval page shows about a sign-in waiting to be approved.
type DeviceRequest struct {
	UserCode   string             `json:"user_code"`
	ClientName string             `json:"client_name"`
	CreatedAt  pgtype.Timestamptz `json:"created_at"`
	ExpiresAt  pgtype.Timestamptz `json:"expires_at"`
}

// CLISession is a CLI that stays signed in, as the account page lists it; the refresh token is never shown again.
type CLISession struct {
	ID         int32              `json:"id"`
	Name       string             `json:"name"`
	CreatedAt  pgtype.Timestamptz `json:"created_at"`
	LastUsedAt pgtype.Timestamptz `json:"last_used_at"`
	ExpiresAt  pgtype.Timestamptz `json:"expires_at"`
}

// CLIGrant is an approved sign-in: who it's for, and the refresh token that keeps the CLI signed in.
type CLIGrant struct {
	User         *UserNoPassword
	RefreshToken string
}

// secret is 32 random bytes as text; only its hash is stored.
func secret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newUserCode() (string, error) {
	// 240 is the largest multiple of the alphabet under a byte, so every letter is as likely as the next.
	limit := byte(256 - 256%len(userCodeAlphabet))
	code := make([]byte, 0, userCodeLength)
	buf := make([]byte, 1)
	for len(code) < userCodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] < limit {
			code = append(code, userCodeAlphabet[int(buf[0])%len(userCodeAlphabet)])
		}
	}
	return string(code), nil
}

// FormatUserCode is the code as shown, in two halves.
func FormatUserCode(code string) string {
	if len(code) != userCodeLength {
		return code
	}
	return code[:userCodeLength/2] + "-" + code[userCodeLength/2:]
}

// NormalizeUserCode takes the code however it was typed: either case, with or without the dash or spaces.
func NormalizeUserCode(typed string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(typed) {
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func cleanClientName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "taco"
	}
	if runes := []rune(name); len(runes) > maxClientName {
		name = string(runes[:maxClientName])
	}
	return name
}

// StartDeviceAuthorization begins a CLI's sign-in: codes for it and for the user, good for DeviceCodeTTL.
func (m *storeModel) StartDeviceAuthorization(ctx context.Context, clientName string, now time.Time) (*DeviceStart, error) {
	deviceCode, err := secret()
	if err != nil {
		return nil, err
	}
	userCode, err := newUserCode()
	if err != nil {
		return nil, err
	}
	expires := now.Add(DeviceCodeTTL)
	err = m.store.InTx(ctx, func(q db.Querier) error {
		if err := q.DeleteExpiredDeviceAuthorizations(ctx, timestamptz(now)); err != nil {
			return err
		}
		waiting, err := q.CountDeviceAuthorizations(ctx)
		if err != nil {
			return err
		}
		if waiting >= maxDeviceAuthorizations {
			return ErrTooManyDevices
		}
		_, err = q.CreateDeviceAuthorization(ctx, &db.CreateDeviceAuthorizationParams{
			DeviceCodeHash: hashSecret(deviceCode), UserCode: userCode, ClientName: cleanClientName(clientName),
			CreatedAt: timestamptz(now), ExpiresAt: timestamptz(expires),
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return &DeviceStart{DeviceCode: deviceCode, UserCode: FormatUserCode(userCode), ExpiresAt: expires}, nil
}

// DeviceRequest finds a sign-in waiting for approval by the code the user typed; not found once it's decided or has run out.
func (m *storeModel) DeviceRequest(ctx context.Context, userCode string, now time.Time) (*DeviceRequest, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	row, err := q.GetPendingDeviceAuthorization(ctx, &db.GetPendingDeviceAuthorizationParams{UserCode: NormalizeUserCode(userCode), ExpiresAt: timestamptz(now)})
	if err != nil {
		return nil, err
	}
	return &DeviceRequest{UserCode: FormatUserCode(row.UserCode), ClientName: row.ClientName, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}, nil
}

// ApproveDevice signs the waiting CLI in as the user; not found if the code isn't waiting any more.
func (m *storeModel) ApproveDevice(ctx context.Context, userCode string, now time.Time, userToken *tokens.UserToken) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	n, err := q.ApproveDeviceAuthorization(ctx, &db.ApproveDeviceAuthorizationParams{UserCode: NormalizeUserCode(userCode), ExpiresAt: timestamptz(now), UserID: &userToken.ID})
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// DenyDevice turns the waiting CLI away. Any signed-in user may: it gives nothing away, and the code was shown to them.
func (m *storeModel) DenyDevice(ctx context.Context, userCode string, now time.Time) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	n, err := q.DenyDeviceAuthorization(ctx, &db.DenyDeviceAuthorizationParams{UserCode: NormalizeUserCode(userCode), ExpiresAt: timestamptz(now)})
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// ExchangeDeviceCode is the CLI's poll: the grant once the user has approved, and until then one of the RFC 8628 errors.
// A device code is good for one grant.
func (m *storeModel) ExchangeDeviceCode(ctx context.Context, deviceCode string, now time.Time) (*CLIGrant, error) {
	var grant *CLIGrant
	// What to answer when there's no grant; kept apart from the transaction's error so that what the poll wrote still commits.
	var answer error
	err := m.store.InTx(ctx, func(q db.Querier) error {
		row, err := q.GetDeviceAuthorizationByHash(ctx, hashSecret(deviceCode))
		if IsNotFound(err) {
			answer = ErrInvalidGrant
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case !row.ExpiresAt.Time.After(now):
			answer = ErrExpiredToken
			return q.DeleteDeviceAuthorization(ctx, row.ID)
		case row.Denied:
			answer = ErrAccessDenied
			return q.DeleteDeviceAuthorization(ctx, row.ID)
		case row.LastPolledAt.Valid && now.Sub(row.LastPolledAt.Time) < DevicePollInterval-devicePollSlack:
			answer = ErrSlowDown
			return nil
		case row.UserID == nil:
			answer = ErrAuthorizationPending
			return q.TouchDeviceAuthorization(ctx, &db.TouchDeviceAuthorizationParams{ID: row.ID, LastPolledAt: timestamptz(now)})
		}
		user, err := q.GetUser(ctx, *row.UserID)
		if err != nil {
			return err
		}
		refresh, err := secret()
		if err != nil {
			return err
		}
		if _, err := q.CreateCliSession(ctx, &db.CreateCliSessionParams{
			UserID: user.ID, Name: row.ClientName, TokenHash: hashSecret(refresh),
			CreatedAt: timestamptz(now), ExpiresAt: timestamptz(now.Add(CLISessionTTL)),
		}); err != nil {
			return err
		}
		grant = &CLIGrant{User: &UserNoPassword{ID: user.ID, Email: user.Email, DisplayName: user.DisplayName}, RefreshToken: refresh}
		return q.DeleteDeviceAuthorization(ctx, row.ID)
	})
	if err != nil {
		return nil, err
	}
	return grant, answer
}

// RefreshCLISession checks a refresh token and says whose it is, starting the session's idle clock again; ErrInvalidGrant if it's unknown, revoked or lapsed.
func (m *storeModel) RefreshCLISession(ctx context.Context, refreshToken string, now time.Time) (*UserNoPassword, error) {
	var user *UserNoPassword
	err := m.store.InTx(ctx, func(q db.Querier) error {
		session, err := q.GetCliSessionByHash(ctx, hashSecret(refreshToken))
		if IsNotFound(err) || (err == nil && !session.ExpiresAt.Time.After(now)) {
			return ErrInvalidGrant
		}
		if err != nil {
			return err
		}
		row, err := q.GetUser(ctx, session.UserID)
		if err != nil {
			return err
		}
		user = &UserNoPassword{ID: row.ID, Email: row.Email, DisplayName: row.DisplayName}
		return q.TouchCliSession(ctx, &db.TouchCliSessionParams{ID: session.ID, LastUsedAt: timestamptz(now), ExpiresAt: timestamptz(now.Add(CLISessionTTL))})
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// RevokeCLISessionByToken is the CLI signing itself out; an unknown token is already signed out.
func (m *storeModel) RevokeCLISessionByToken(ctx context.Context, refreshToken string) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	return q.DeleteCliSessionByHash(ctx, hashSecret(refreshToken))
}

func (m *storeModel) CLISessions(ctx context.Context, userToken *tokens.UserToken) ([]*CLISession, error) {
	q, err := m.store.GetQuerier()
	if err != nil {
		return nil, err
	}
	rows, err := q.ListCliSessionsByUser(ctx, userToken.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*CLISession, 0, len(rows))
	for _, r := range rows {
		out = append(out, &CLISession{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt, ExpiresAt: r.ExpiresAt})
	}
	return out, nil
}

// RevokeCLISession ends one of the user's sessions; its CLI keeps working until its current access token runs out.
func (m *storeModel) RevokeCLISession(ctx context.Context, id int32, userToken *tokens.UserToken) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	n, err := q.DeleteCliSession(ctx, &db.DeleteCliSessionParams{ID: id, UserID: userToken.ID})
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// PruneCLIAuth deletes sign-ins that ran out unapproved and sessions that lapsed unused.
func (m *storeModel) PruneCLIAuth(ctx context.Context, now time.Time) error {
	q, err := m.store.GetQuerier()
	if err != nil {
		return err
	}
	if err := q.DeleteExpiredDeviceAuthorizations(ctx, timestamptz(now)); err != nil {
		return err
	}
	return q.DeleteLapsedCliSessions(ctx, timestamptz(now))
}
