package tokens_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/config"
	"gitea.deepak.science/deepak/taiga/internal/tokens"
)

// What a toker in the "test" environment issues and expects.
const (
	testIssuer   = "trygo-test"
	testAudience = "trygo-test-users"
)

// One keypair for the package: generating RSA keys is the slow part.
var testKey = func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
}()

func writeKeys(t *testing.T, key *rsa.PrivateKey) config.TokensConfig {
	t.Helper()
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	dir := t.TempDir()
	cfg := config.TokensConfig{PrivateKeyPath: filepath.Join(dir, "test.key"), PublicKeyPath: filepath.Join(dir, "test.pem")}
	require.NoError(t, os.WriteFile(cfg.PrivateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))
	require.NoError(t, os.WriteFile(cfg.PublicKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}), 0o600))
	return cfg
}

func newToker(t *testing.T, environment string, key *rsa.PrivateKey) tokens.Toker {
	t.Helper()
	toker, err := tokens.New(config.Config{App: config.AppConfig{Environment: environment}, Tokens: writeKeys(t, key)})
	require.NoError(t, err)
	return toker
}

// goodClaims are what the test toker would issue for alice; each case below breaks one thing.
func goodClaims() jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"email": "alice@example.com",
		"id":    7,
		"iss":   testIssuer,
		"aud":   []string{testAudience},
		"iat":   now.Unix(),
		"nbf":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
}

func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(method, claims).SignedString(key)
	require.NoError(t, err)
	return signed
}

func TestRoundTrip(t *testing.T) {
	toker := newToker(t, "test", testKey)

	signed, err := toker.EncodeUser(&tokens.UserToken{ID: 7, Email: "alice@example.com"})
	require.NoError(t, err)

	user, err := toker.DecodeTokenString(signed)
	require.NoError(t, err)
	assert.Equal(t, &tokens.UserToken{ID: 7, Email: "alice@example.com"}, user)

	parsed, _, err := jwt.NewParser().ParseUnverified(signed, jwt.MapClaims{})
	require.NoError(t, err)
	exp, err := parsed.Claims.GetExpirationTime()
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(tokens.Lifetime), exp.Time, time.Minute)
}

func TestForgedClaimsDecode(t *testing.T) {
	// The forging helper has to make tokens the toker takes, or the rejections below prove nothing.
	toker := newToker(t, "test", testKey)
	user, err := toker.DecodeTokenString(sign(t, jwt.SigningMethodPS256, testKey, goodClaims()))
	require.NoError(t, err)
	assert.Equal(t, int32(7), user.ID)
}

func TestDecodeRejects(t *testing.T) {
	toker := newToker(t, "test", testKey)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	with := func(change func(jwt.MapClaims)) jwt.MapClaims {
		claims := goodClaims()
		change(claims)
		return claims
	}

	for name, token := range map[string]string{
		"not a token":                   "definitely.not.ajwt",
		"empty":                         "",
		"another key":                   sign(t, jwt.SigningMethodPS256, otherKey, goodClaims()),
		"RS256":                         sign(t, jwt.SigningMethodRS256, testKey, goodClaims()),
		"HS256 keyed on the public key": sign(t, jwt.SigningMethodHS256, x509.MarshalPKCS1PublicKey(&testKey.PublicKey), goodClaims()),
		"unsigned":                      sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, goodClaims()),
		"expired":                       sign(t, jwt.SigningMethodPS256, testKey, with(func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() })),
		"no expiry":                     sign(t, jwt.SigningMethodPS256, testKey, with(func(c jwt.MapClaims) { delete(c, "exp") })),
		"not yet valid":                 sign(t, jwt.SigningMethodPS256, testKey, with(func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(time.Hour).Unix() })),
		"issued in the future":          sign(t, jwt.SigningMethodPS256, testKey, with(func(c jwt.MapClaims) { c["iat"] = time.Now().Add(time.Hour).Unix() })),
		"another issuer":                sign(t, jwt.SigningMethodPS256, testKey, with(func(c jwt.MapClaims) { c["iss"] = "someone-else" })),
		"another audience":              sign(t, jwt.SigningMethodPS256, testKey, with(func(c jwt.MapClaims) { c["aud"] = []string{"someone-else"} })),
		"no email":                      sign(t, jwt.SigningMethodPS256, testKey, with(func(c jwt.MapClaims) { delete(c, "email") })),
	} {
		t.Run(name, func(t *testing.T) {
			user, err := toker.DecodeTokenString(token)
			assert.Error(t, err)
			assert.Nil(t, user)
		})
	}
}

func TestTokensDoNotCrossEnvironments(t *testing.T) {
	// Same key, as when one keypair is reused: the issuer and audience are what keep a staging token out of production.
	staging := newToker(t, "staging", testKey)
	production := newToker(t, "production", testKey)

	signed, err := staging.EncodeUser(&tokens.UserToken{ID: 1, Email: "alice@example.com"})
	require.NoError(t, err)

	_, err = staging.DecodeTokenString(signed)
	require.NoError(t, err)
	_, err = production.DecodeTokenString(signed)
	assert.Error(t, err)
}

func TestNewNeedsBothKeys(t *testing.T) {
	good := writeKeys(t, testKey)
	junk := filepath.Join(t.TempDir(), "junk.pem")
	require.NoError(t, os.WriteFile(junk, []byte("not a key"), 0o600))

	for name, cfg := range map[string]config.TokensConfig{
		"missing private key":    {PrivateKeyPath: filepath.Join(t.TempDir(), "absent"), PublicKeyPath: good.PublicKeyPath},
		"missing public key":     {PrivateKeyPath: good.PrivateKeyPath, PublicKeyPath: filepath.Join(t.TempDir(), "absent")},
		"unreadable private key": {PrivateKeyPath: junk, PublicKeyPath: good.PublicKeyPath},
		"unreadable public key":  {PrivateKeyPath: good.PrivateKeyPath, PublicKeyPath: junk},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tokens.New(config.Config{Tokens: cfg})
			assert.Error(t, err)
		})
	}
}

func TestAuthenticator(t *testing.T) {
	toker := newToker(t, "test", testKey)
	signed, err := toker.EncodeUser(&tokens.UserToken{ID: 7, Email: "alice@example.com"})
	require.NoError(t, err)

	var seen *tokens.UserToken
	handler := toker.Authenticator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := tokens.UserTokenFromContext(r.Context())
		require.NoError(t, err)
		seen = user
		w.WriteHeader(http.StatusNoContent)
	}))

	for name, tc := range map[string]struct {
		header string
		status int
	}{
		"bearer":                           {"Bearer " + signed, http.StatusNoContent},
		"lowercase scheme":                 {"bearer " + signed, http.StatusNoContent},
		"no header":                        {"", http.StatusUnauthorized},
		"scheme alone":                     {"Bearer ", http.StatusUnauthorized},
		"another scheme":                   {"Basic " + signed, http.StatusUnauthorized},
		"bare token":                       {signed, http.StatusUnauthorized},
		"not a token":                      {"Bearer nope", http.StatusUnauthorized},
		"claims without the standard ones": {"Bearer " + sign(t, jwt.SigningMethodPS256, testKey, jwt.MapClaims{"email": "alice@example.com", "id": 7}), http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			seen = nil
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			assert.Equal(t, tc.status, w.Code)
			if tc.status == http.StatusNoContent {
				assert.Equal(t, &tokens.UserToken{ID: 7, Email: "alice@example.com"}, seen)
			} else {
				assert.Nil(t, seen, "the handler behind the middleware never ran")
			}
		})
	}
}

func TestUserTokenFromContextWithoutOne(t *testing.T) {
	_, err := tokens.UserTokenFromContext(httptest.NewRequest(http.MethodGet, "/", nil).Context())
	assert.Error(t, err)
}
