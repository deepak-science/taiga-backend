// Package sources connects a user's account elsewhere (COROS, for now) and keeps their workouts in step with it (docs/sources.md).
// It is the glue: internal/coros speaks to COROS, internal/models stores, and this holds the keys between them.
package sources

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"gitea.deepak.science/deepak/taiga/internal/coros"
	"gitea.deepak.science/deepak/taiga/internal/filerepo"
	"gitea.deepak.science/deepak/taiga/internal/models"
	"gitea.deepak.science/deepak/taiga/internal/secretbox"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

const (
	cookieName = "taiga_source"
	// Long enough to sign in at COROS.
	flowTTL = 10 * time.Minute
	// A sync has to finish inside its job's lease, or a second one would start beside it and spend the same refresh token.
	syncTimeout = 4 * time.Minute
)

// ErrFlow means the callback doesn't belong to a connection this server started, or it took too long; the user should start again.
var ErrFlow = errors.New("connecting expired or didn't start here; try again")

// ProviderError is an error COROS sent back instead of a code, e.g. the user declined.
type ProviderError struct{ Code string }

func (e *ProviderError) Error() string { return "COROS said " + e.Code }

// Manager connects users to COROS and syncs them.
type Manager struct {
	m     models.Model
	coros *coros.Service
	// Two boxes from one secret: what's stored never opens as a cookie, nor the other way round.
	stored *secretbox.Box
	flows  *secretbox.Box

	publicBase   string
	callbackPath string
	secure       bool
	// Now is the clock, swappable in tests.
	Now func() time.Time
}

// New returns a manager whose callback is under apiBase and which sends browsers back to the frontend at publicBase.
// secret seals the tokens it stores and its flow cookie.
func New(m models.Model, endpoint, publicBase, apiBase string, secret []byte) (*Manager, error) {
	if publicBase == "" {
		return nil, errors.New("auth.public_base_url must be set to connect sources")
	}
	callback, err := url.Parse(strings.TrimSuffix(apiBase, "/") + "/sources/" + coros.Kind + "/callback")
	if err != nil {
		return nil, fmt.Errorf("auth.api_base_url: %w", err)
	}
	return &Manager{
		m:            m,
		coros:        coros.New(endpoint, callback.String()),
		stored:       secretbox.New(secret, "taiga source credentials"),
		flows:        secretbox.New(secret, "taiga source flow"),
		publicBase:   strings.TrimSuffix(publicBase, "/"),
		callbackPath: "/" + strings.TrimPrefix(callback.Path, "/"),
		secure:       callback.Scheme == "https",
		Now:          time.Now,
	}, nil
}

// FrontendURL is a frontend route, e.g. "#/account?source=coros", as an absolute URL.
func (g *Manager) FrontendURL(fragment string) string {
	return g.publicBase + "/" + fragment
}

// flow is what the callback needs to know it answers this browser's request.
type flow struct {
	User     int32  `json:"u"`
	Client   int32  `json:"c"`
	State    string `json:"s"`
	Verifier string `json:"v"`
	Expires  int64  `json:"e"`
}

// Begin starts connecting the user: it sets the flow cookie on w and returns the COROS address to send the browser to.
// The first time, it registers this deployment with COROS.
func (g *Manager) Begin(ctx context.Context, w http.ResponseWriter, userToken *tokens.UserToken) (string, error) {
	row, client, err := g.client(ctx)
	if err != nil {
		return "", err
	}
	f := flow{User: userToken.ID, Client: row.ID, State: random(), Verifier: oauth2.GenerateVerifier(), Expires: g.Now().Add(flowTTL).Unix()}
	body, _ := json.Marshal(f)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    g.flows.Seal(body),
		Path:     g.callbackPath,
		MaxAge:   int(flowTTL.Seconds()),
		HttpOnly: true,
		Secure:   g.secure,
		// Lax still sends it on COROS's top-level redirect back.
		SameSite: http.SameSiteLaxMode,
	})
	return g.coros.AuthCodeURL(client, f.State, f.Verifier), nil
}

// client finds where COROS signs users in and this deployment's registration there, registering if there is none.
func (g *Manager) client(ctx context.Context) (*models.SourceClient, *coros.Client, error) {
	found, err := g.coros.Discover(ctx)
	if err != nil {
		return nil, nil, err
	}
	row, err := g.m.SourceClient(ctx, coros.Kind, found.Issuer, g.coros.RedirectURL())
	if err == nil {
		client, err := g.openClient(row)
		return row, client, err
	}
	if !models.IsNotFound(err) {
		return nil, nil, err
	}
	if err := g.coros.Register(ctx, found); err != nil {
		return nil, nil, err
	}
	body, _ := json.Marshal(found)
	row, err = g.m.SaveSourceClient(ctx, coros.Kind, found.Issuer, g.coros.RedirectURL(), g.stored.Seal(body))
	if err != nil {
		return nil, nil, err
	}
	log.Printf("sources: registered with COROS at %s", found.Issuer)
	// Another request may have registered first, and its row is the one kept.
	client, err := g.openClient(row)
	return row, client, err
}

func (g *Manager) openClient(row *models.SourceClient) (*coros.Client, error) {
	body, err := g.stored.Open(row.Registration)
	if err != nil {
		return nil, fmt.Errorf("the stored COROS registration can't be read (was the token signing key changed?): %w", err)
	}
	var client coros.Client
	if err := json.Unmarshal(body, &client); err != nil {
		return nil, err
	}
	return &client, nil
}

// Finish checks the callback against its flow cookie, exchanges the code and keeps the tokens, which queues the first sync.
// It clears the cookie either way, so a callback can't be replayed.
func (g *Manager) Finish(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: g.callbackPath, MaxAge: -1, HttpOnly: true, Secure: g.secure, SameSite: http.SameSiteLaxMode})
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return ErrFlow
	}
	body, err := g.flows.Open(cookie.Value)
	if err != nil {
		return ErrFlow
	}
	var f flow
	if err := json.Unmarshal(body, &f); err != nil || g.Now().Unix() > f.Expires {
		return ErrFlow
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(f.State)) != 1 {
		return ErrFlow
	}
	if code := q.Get("error"); code != "" {
		return &ProviderError{Code: code}
	}

	row, err := g.m.SourceClientByID(ctx, f.Client)
	if err != nil {
		return err
	}
	client, err := g.openClient(row)
	if err != nil {
		return err
	}
	token, err := g.coros.Exchange(ctx, client, q.Get("code"), f.Verifier)
	if err != nil {
		return err
	}
	_, err = g.m.ConnectSource(ctx, coros.Kind, row.ID, g.seal(token), g.Now(), &tokens.UserToken{ID: f.User})
	if err == nil {
		log.Printf("sources: connected user %d to COROS", f.User)
	}
	return err
}

// storedToken is the part of a token worth keeping.
type storedToken struct {
	Access  string    `json:"a"`
	Refresh string    `json:"r"`
	Expiry  time.Time `json:"e"`
}

func (g *Manager) seal(token *oauth2.Token) string {
	body, _ := json.Marshal(storedToken{Access: token.AccessToken, Refresh: token.RefreshToken, Expiry: token.Expiry})
	return g.stored.Seal(body)
}

func (g *Manager) open(credentials string) (*oauth2.Token, error) {
	body, err := g.stored.Open(credentials)
	if err != nil {
		return nil, err
	}
	var t storedToken
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, err
	}
	return &oauth2.Token{AccessToken: t.Access, RefreshToken: t.Refresh, Expiry: t.Expiry, TokenType: "Bearer"}, nil
}

// Disconnect forgets the user's connection, ending the grant at COROS if COROS can be reached; what was imported stays.
func (g *Manager) Disconnect(ctx context.Context, id int32, userToken *tokens.UserToken) error {
	conn, err := g.m.SourceConnection(ctx, id, userToken)
	if err != nil {
		return err
	}
	// Best effort: the tokens are deleted here whatever COROS says, and it lists connected apps for the user to remove.
	if err := g.revoke(ctx, conn); err != nil {
		log.Printf("sources: could not revoke user %d's grant at COROS: %v", userToken.ID, err)
	}
	return g.m.DisconnectSource(ctx, id, userToken)
}

func (g *Manager) revoke(ctx context.Context, conn *models.SourceConnection) error {
	row, err := g.m.SourceClientByID(ctx, conn.ClientID)
	if err != nil {
		return err
	}
	client, err := g.openClient(row)
	if err != nil {
		return err
	}
	token, err := g.open(conn.Credentials)
	if err != nil {
		return err
	}
	return g.coros.Revoke(ctx, client, token)
}

// Sync brings the user's new COROS activities in. A user with no connection, or one waiting to be reconnected, has nothing to do.
// A grant COROS has ended is recorded on the connection for the user to see, and isn't an error: trying again can't help.
func (g *Manager) Sync(ctx context.Context, fileRepo filerepo.FileRepo, userToken *tokens.UserToken) (*models.SyncResult, error) {
	conn, err := g.m.SourceConnectionByKind(ctx, coros.Kind, userToken)
	if models.IsNotFound(err) {
		return &models.SyncResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	if conn.NeedsReconnect {
		return &models.SyncResult{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()

	res, err := g.sync(ctx, conn, fileRepo, userToken)
	if err == nil {
		log.Printf("sources: synced user %d with COROS: %+v", userToken.ID, *res)
		return res, nil
	}
	gone := errors.Is(err, coros.ErrAuthorization)
	// Recorded past the deadline that may be what failed the sync.
	if failErr := g.m.FailSourceSync(context.WithoutCancel(ctx), conn.ID, err.Error(), gone, userToken); failErr != nil {
		return nil, errors.Join(err, failErr)
	}
	if gone {
		log.Printf("sources: COROS turned away user %d's connection; it needs connecting again", userToken.ID)
		return &models.SyncResult{}, nil
	}
	return res, err
}

func (g *Manager) sync(ctx context.Context, conn *models.SourceConnection, fileRepo filerepo.FileRepo, userToken *tokens.UserToken) (*models.SyncResult, error) {
	row, err := g.m.SourceClientByID(ctx, conn.ClientID)
	if err != nil {
		return nil, err
	}
	client, err := g.openClient(row)
	if err != nil {
		return nil, err
	}
	token, err := g.open(conn.Credentials)
	if err != nil {
		return nil, fmt.Errorf("the stored COROS tokens can't be read (was the token signing key changed?): %w", coros.ErrAuthorization)
	}
	session, err := g.coros.Open(ctx, client, token)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	// Opening may have refreshed the tokens, and COROS's refresh tokens work once: the new one is saved before anything else can fail.
	save := func() error {
		fresh, err := session.Token()
		if err != nil {
			return err
		}
		if fresh.AccessToken == token.AccessToken && fresh.RefreshToken == token.RefreshToken {
			return nil
		}
		token = fresh
		return g.m.SetSourceCredentials(context.WithoutCancel(ctx), conn.ID, g.seal(fresh), userToken)
	}
	if err := save(); err != nil {
		return nil, err
	}
	res, err := g.m.SyncSource(ctx, conn, session, fileRepo, g.Now(), userToken)
	return res, errors.Join(err, save())
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
