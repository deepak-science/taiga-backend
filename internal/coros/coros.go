// Package coros reads a user's activities from COROS through its MCP server (docs/sources.md): OAuth to sign the user in, then two of the server's tools, one to list activities and one to hand over their FIT files.
package coros

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

const (
	// Kind names this source in the database and the API.
	Kind = "coros"
	// DefaultEndpoint sends each account on to its region's server.
	DefaultEndpoint = "https://mcp.coros.com/mcp"

	// The server's tools also write to the account (training plans, scheduled workouts); these two reads are the only ones this package may call.
	toolList     = "querySportRecords"
	toolFileURLs = "queryActivityFitFileDownloadUrls"

	// More than a window should ever hold; a full page means some were left out.
	listLimit = 200
	// The size an upload may be.
	maxFileBytes = 32 << 20
	timeout      = 30 * time.Second
)

var (
	// ErrAuthorization means COROS no longer honours the connection's tokens; the user has to connect again.
	ErrAuthorization = errors.New("COROS no longer accepts this connection; connect it again")
	// ErrDailyLimit means COROS has handed over all the files it will today.
	ErrDailyLimit = errors.New("COROS's daily limit on FIT files is used up")
	// ErrInProgress means the activity hasn't finished, so it has no file yet.
	ErrInProgress = errors.New("the activity is still in progress")
	// ErrUnexpectedReply means a tool answered in words this package can't read: its replies are prose, so this is what a change of wording looks like.
	ErrUnexpectedReply = errors.New("COROS answered in a form taiga doesn't know how to read")
)

// Service talks to one COROS MCP endpoint on behalf of this deployment.
type Service struct {
	endpoint    string
	redirectURL string
	http        *http.Client
}

// New returns a service for the MCP server at endpoint (empty for COROS's own); redirectURL is where COROS sends the browser back after sign-in.
func New(endpoint, redirectURL string) *Service {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Service{endpoint: endpoint, redirectURL: redirectURL, http: &http.Client{Timeout: timeout}}
}

// RedirectURL is the callback registered with COROS.
func (s *Service) RedirectURL() string { return s.redirectURL }

// Client is this deployment as COROS knows it, with the addresses of the server it's registered at.
type Client struct {
	// Resource is the MCP address to call, which is the region's and not always the one configured.
	Resource      string `json:"resource"`
	Issuer        string `json:"issuer"`
	AuthURL       string `json:"auth_url"`
	TokenURL      string `json:"token_url"`
	RevocationURL string `json:"revocation_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	// Where Register signs up; only Discover sets it.
	RegistrationURL string `json:"-"`
}

// Discover follows the endpoint's metadata to where users sign in, leaving ClientID empty for Register to fill.
func (s *Service) Discover(ctx context.Context) (*Client, error) {
	metadataURL, err := s.resourceMetadataURL(ctx)
	if err != nil {
		return nil, err
	}
	// Not oauthex's fetch: that insists the resource is the address asked, and mcp.coros.com answers for its regional servers.
	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := s.getJSON(ctx, metadataURL, &resource); err != nil {
		return nil, fmt.Errorf("reading the resource metadata: %w", err)
	}
	if resource.Resource == "" || len(resource.AuthorizationServers) == 0 {
		return nil, errors.New("the resource metadata names no resource or authorization server")
	}
	issuer := resource.AuthorizationServers[0]
	for _, u := range []string{resource.Resource, issuer} {
		if err := s.checkURL(u); err != nil {
			return nil, err
		}
	}
	meta, err := oauthex.GetAuthServerMeta(ctx, strings.TrimSuffix(issuer, "/")+"/.well-known/oauth-authorization-server", issuer, s.http)
	if err != nil {
		return nil, fmt.Errorf("reading the authorization server metadata: %w", err)
	}
	if meta == nil {
		return nil, errors.New("the authorization server publishes no metadata")
	}
	if meta.RegistrationEndpoint == "" {
		return nil, errors.New("the authorization server doesn't register clients")
	}
	return &Client{
		Resource:      resource.Resource,
		Issuer:        meta.Issuer,
		AuthURL:       meta.AuthorizationEndpoint,
		TokenURL:      meta.TokenEndpoint,
		RevocationURL: meta.RevocationEndpoint,

		RegistrationURL: meta.RegistrationEndpoint,
	}, nil
}

// resourceMetadataURL asks the endpoint without a token, which it answers with where its metadata is.
func (s *Service) resourceMetadataURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("reaching the COROS server: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate")); err == nil {
		for _, c := range challenges {
			if u := c.Params["resource_metadata"]; u != "" {
				return u, s.checkURL(u)
			}
		}
	}
	// A server that doesn't say gets asked at the well-known place for its path.
	u, err := url.Parse(s.endpoint)
	if err != nil {
		return "", err
	}
	u.Path = "/.well-known/oauth-protected-resource" + u.Path
	return u.String(), nil
}

// checkURL keeps metadata from sending tokens anywhere less protected than the endpoint itself.
func (s *Service) checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("the COROS server named an address that isn't one: %w", err)
	}
	if u.Scheme != "https" && !strings.HasPrefix(s.endpoint, "http://") {
		return fmt.Errorf("the COROS server named an address that isn't https (%s)", u.Host)
	}
	return nil
}

func (s *Service) getJSON(ctx context.Context, address string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into)
}

// Register makes this deployment a client of the server Discover found, and fills in the credentials it's given.
func (s *Service) Register(ctx context.Context, c *Client) error {

	resp, err := oauthex.RegisterClient(ctx, c.RegistrationURL, &oauthex.ClientRegistrationMetadata{
		RedirectURIs: []string{s.redirectURL},
		ClientName:   "taiga",
		GrantTypes:   []string{"authorization_code", "refresh_token"},
		// Asked for, since some servers give a client without a secret no refresh token. COROS registers one without regardless, and refreshes it anyway.
		TokenEndpointAuthMethod: "client_secret_basic",
		ResponseTypes:           []string{"code"},
		Scope:                   strings.Join(scopes, " "),
	}, s.http)
	if err != nil {
		return fmt.Errorf("registering with COROS: %w", err)
	}
	c.ClientID, c.ClientSecret = resp.ClientID, resp.ClientSecret
	return nil
}

// offline_access is what gets a refresh token; mcp.tools covers every tool, there being no narrower scope.
var scopes = []string{"mcp.tools", "offline_access"}

func (s *Service) oauth(c *Client) *oauth2.Config {
	style := oauth2.AuthStyleInHeader
	if c.ClientSecret == "" {
		style = oauth2.AuthStyleInParams
	}
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: c.AuthURL, TokenURL: c.TokenURL, AuthStyle: style},
		RedirectURL:  s.redirectURL,
		Scopes:       scopes,
	}
}

// AuthCodeURL is where to send the browser to sign in; verifier is the PKCE secret Exchange will need.
func (s *Service) AuthCodeURL(c *Client, state, verifier string) string {
	return s.oauth(c).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", c.Resource))
}

// Exchange turns the code COROS sent back into tokens.
func (s *Service) Exchange(ctx context.Context, c *Client, code, verifier string) (*oauth2.Token, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.http)
	token, err := s.oauth(c).Exchange(ctx, code, oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", c.Resource))
	if err != nil {
		return nil, fmt.Errorf("exchanging the code: %w", err)
	}
	if token.RefreshToken == "" {
		return nil, errors.New("COROS gave no refresh token, so the connection would lapse with its first token")
	}
	return token, nil
}

// Revoke ends the grant at COROS, as far as it can: the refresh token goes, and the access tokens with it if the server works that way.
func (s *Service) Revoke(ctx context.Context, c *Client, token *oauth2.Token) error {
	if c.RevocationURL == "" || token.RefreshToken == "" {
		return nil
	}
	form := url.Values{"token": {token.RefreshToken}, "token_type_hint": {"refresh_token"}}
	if c.ClientSecret == "" {
		form.Set("client_id", c.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.RevocationURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.ClientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(c.ClientSecret))
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revoking at COROS: status %d", resp.StatusCode)
	}
	return nil
}

// Session is one signed-in conversation with the server.
type Session struct {
	mcp     *mcp.ClientSession
	tokens  oauth2.TokenSource
	refused *refusals
	// For the files, which are fetched from links that need no token and must not be sent one.
	plain    *http.Client
	insecure bool
}

// Open signs in to the server with the connection's tokens, refreshing them if they've run out.
// The tokens may be new afterwards: save what Token returns.
func (s *Service) Open(ctx context.Context, c *Client, token *oauth2.Token) (*Session, error) {
	refreshCtx := context.WithValue(context.WithoutCancel(ctx), oauth2.HTTPClient, s.http)
	source := oauth2.ReuseTokenSource(token, s.oauth(c).TokenSource(refreshCtx, token))
	refused := &refusals{base: &oauth2.Transport{Source: source, Base: s.http.Transport}}
	authed := &http.Client{Timeout: timeout, Transport: refused}

	client := mcp.NewClient(&mcp.Implementation{Name: "taiga", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   c.Resource,
		HTTPClient: authed,
		// Nothing here listens for what the server might say unasked.
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, refused.authError(fmt.Errorf("connecting to COROS: %w", err))
	}
	return &Session{mcp: session, tokens: source, refused: refused, plain: s.http, insecure: strings.HasPrefix(s.endpoint, "http://")}, nil
}

// Token is the connection's tokens as they are now.
func (s *Session) Token() (*oauth2.Token, error) {
	token, err := s.tokens.Token()
	return token, s.refused.authError(err)
}

func (s *Session) Close() { _ = s.mcp.Close() }

// call runs one of the two tools and returns what it said.
func (s *Session) call(ctx context.Context, tool string, args map[string]any) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := s.mcp.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", false, s.refused.authError(fmt.Errorf("calling %s: %w", tool, err))
	}
	var text strings.Builder
	for _, content := range result.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			text.WriteString(t.Text)
		}
	}
	return unquote(text.String()), result.IsError, nil
}

// unquote undoes a reply sent as a JSON string, quotes and escapes included, which is how COROS sends the list (and not the file links).
func unquote(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, `"`) {
		var inner string
		if json.Unmarshal([]byte(trimmed), &inner) == nil {
			return inner
		}
	}
	return text
}

// List returns the activities starting between from and to, oldest first.
// COROS takes whole days in the account's timezone, which isn't known here, so it's asked for a day more at each end and the answer is cut to the window.
func (s *Session) List(ctx context.Context, from, to time.Time) ([]Activity, error) {
	text, failed, err := s.call(ctx, toolList, map[string]any{
		"startDate": from.UTC().AddDate(0, 0, -1).Format("20060102"),
		"endDate":   to.UTC().AddDate(0, 0, 1).Format("20060102"),
		"limit":     listLimit,
	})
	if err != nil {
		return nil, err
	}
	if failed {
		return nil, fmt.Errorf("COROS couldn't list activities: %s", brief(text))
	}
	all, err := parseActivities(text)
	if err != nil {
		return nil, err
	}
	if len(all) >= listLimit {
		return nil, fmt.Errorf("COROS listed %d activities, as many as it was asked for, so there may be more; ask for a shorter window", len(all))
	}
	var within []Activity
	for _, a := range all {
		// One that states no start is kept: better dealt with twice than never.
		if a.Start.IsZero() || (!a.Start.Before(from) && !a.Start.After(to)) {
			within = append(within, a)
		}
	}
	return within, nil
}

// Files downloads the activity's FIT files: one, except for an activity recorded in parts.
// Each counts against COROS's daily limit, whether or not it was fetched before.
func (s *Session) Files(ctx context.Context, a Activity) ([][]byte, error) {
	text, failed, err := s.call(ctx, toolFileURLs, map[string]any{"labelId": a.ID, "sportType": a.SportType})
	if err != nil {
		return nil, err
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "still in progress"):
		return nil, ErrInProgress
	// Only a refusal mentions the limit; a reply with links doesn't.
	case strings.Contains(lower, "limit") && len(fileURLs(text)) == 0:
		return nil, ErrDailyLimit
	case failed:
		return nil, fmt.Errorf("COROS couldn't give the file for activity %s: %s", a.ID, brief(text))
	}
	links := fileURLs(text)
	if len(links) == 0 {
		return nil, fmt.Errorf("%w: no download link for activity %s in %q", ErrUnexpectedReply, a.ID, brief(text))
	}
	var files [][]byte
	for _, link := range links {
		content, err := s.download(ctx, link)
		if err != nil {
			return nil, fmt.Errorf("downloading the file for activity %s: %w", a.ID, err)
		}
		files = append(files, content)
	}
	return files, nil
}

// download fetches a file link. The link works for anyone who has it, so it's kept out of every error and log.
func (s *Session) download(ctx context.Context, link string) ([]byte, error) {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && !s.insecure) {
		return nil, errors.New("the link isn't an https address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, errors.New("the link isn't an address")
	}
	resp, err := s.plain.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxFileBytes {
		return nil, errors.New("the file is larger than an upload may be")
	}
	return content, nil
}

// brief is a reply cut short enough to put in an error.
func brief(text string) string {
	text = strings.Join(strings.Fields(linkPattern.ReplaceAllString(text, "[link]")), " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

// refusals notes when the server answers 401, which the MCP client reports only as text.
type refusals struct {
	base http.RoundTripper
	seen atomic.Bool
}

func (r *refusals) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		r.seen.Store(true)
	}
	return resp, err
}

// authError marks err as ErrAuthorization when the grant is gone (a refresh the server won't honour, or a token it refused), as opposed to COROS being unreachable.
func (r *refusals) authError(err error) error {
	if err == nil {
		return nil
	}
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) && (retrieve.ErrorCode == "invalid_grant" || retrieve.ErrorCode == "invalid_client") {
		return fmt.Errorf("%w (%s)", ErrAuthorization, retrieve.ErrorCode)
	}
	if r.seen.Load() {
		return fmt.Errorf("%w (the server refused its token)", ErrAuthorization)
	}
	return err
}
