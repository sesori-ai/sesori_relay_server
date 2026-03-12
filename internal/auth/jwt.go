package auth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/anthropics/remote-relay/internal/protocol"
	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
)

const (
	tokenTypeAccess = "access"
	tokenTypeBridge = "bridge"
	audienceMobile  = "mobile"
	audienceBridge  = "bridge"
	issuerBackend   = "auth-backend"
)

// JWTAuthenticator validates WebSocket connections by reading an auth message
// containing a signed RS256 JWT. It verifies the signature against the key
// store, validates required claims, and returns the authenticated identity.
type JWTAuthenticator struct {
	keyStore *KeyStore
}

// NewJWTAuthenticator creates an authenticator backed by the given key store.
func NewJWTAuthenticator(keyStore *KeyStore) *JWTAuthenticator {
	return &JWTAuthenticator{keyStore: keyStore}
}

// Authenticate reads the first WebSocket message, validates it as a RoleAuthMessage
// with a valid RS256 JWT, and returns the authenticated result. On failure the
// connection is closed with an appropriate close code.
func (a *JWTAuthenticator) Authenticate(ctx context.Context, conn *websocket.Conn) (AuthResult, error) {
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, data, err := conn.Read(authCtx)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthRequired), "auth timeout")
		return AuthResult{}, fmt.Errorf("auth read failed: %w", err)
	}

	parsed, err := protocol.ParseMessage(data)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "expected auth message")
		return AuthResult{}, fmt.Errorf("failed to parse auth message: %w", err)
	}

	authMsg, ok := parsed.(protocol.RoleAuthMessage)
	if !ok {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), "expected auth message")
		return AuthResult{}, fmt.Errorf("expected RoleAuthMessage, got %T", parsed)
	}

	token, err := a.verifyToken(authMsg.Token)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), fmt.Sprintf("invalid token: %v", err))
		return AuthResult{}, fmt.Errorf("JWT verification failed: %w", err)
	}

	result, tokenType, err := validateClaims(token)
	if err != nil {
		_ = conn.Close(websocket.StatusCode(protocol.CloseAuthFailure), err.Error())
		return AuthResult{}, err
	}

	slog.Debug("connection authenticated", "userId", result.UserID, "tokenType", tokenType)
	return result, nil
}

func (a *JWTAuthenticator) Validate(raw string) (AuthResult, error) {
	token, err := a.verifyToken(raw)
	if err != nil {
		return AuthResult{}, fmt.Errorf("JWT verification failed: %w", err)
	}

	result, _, err := validateClaims(token)
	if err != nil {
		return AuthResult{}, err
	}

	return result, nil
}

// verifyToken parses and verifies the JWT signature. On failure it tries one
// key refresh before giving up, which handles key rotation gracefully.
func (a *JWTAuthenticator) verifyToken(raw string) (*jwt.Token, error) {
	parseWithKey := func() (*jwt.Token, error) {
		return jwt.Parse(raw, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return a.keyStore.PublicKey(), nil
		})
	}

	token, err := parseWithKey()
	if err != nil {
		if refreshErr := a.keyStore.Refresh(); refreshErr != nil {
			slog.Warn("failed to refresh auth public key after verification failure", "err", refreshErr)
		} else {
			token, err = parseWithKey()
		}
	}
	return token, err
}

// validateClaims extracts and validates the required JWT claims. Returns the
// auth result and token type (for logging). Accepted token types are "access"
// and "bridge"; "refresh" tokens are rejected.
func validateClaims(token *jwt.Token) (AuthResult, string, error) {
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return AuthResult{}, "", fmt.Errorf("invalid JWT claims type")
	}

	userId, err := requiredStringClaim(claims, "userId")
	if err != nil {
		return AuthResult{}, "", err
	}
	if userId == "" {
		return AuthResult{}, "", fmt.Errorf("userId claim is empty")
	}

	tokenType, err := requiredStringClaim(claims, "tokenType")
	if err != nil {
		return AuthResult{}, "", err
	}
	if tokenType != tokenTypeAccess && tokenType != tokenTypeBridge {
		return AuthResult{}, "", fmt.Errorf("invalid tokenType claim: %s", tokenType)
	}

	aud, err := audienceClaim(claims)
	if err != nil {
		return AuthResult{}, "", err
	}
	if aud != audienceMobile && aud != audienceBridge {
		return AuthResult{}, "", fmt.Errorf("invalid aud claim: %s", aud)
	}

	iss, err := requiredStringClaim(claims, "iss")
	if err != nil {
		return AuthResult{}, "", err
	}
	if iss != issuerBackend {
		return AuthResult{}, "", fmt.Errorf("invalid iss claim: %s", iss)
	}

	expFloat, err := requiredFloatClaim(claims, "exp")
	if err != nil {
		return AuthResult{}, "", err
	}

	return AuthResult{
		UserID: userId,
		Expiry: time.Unix(int64(expFloat), 0),
	}, tokenType, nil
}

// requiredStringClaim extracts a required string claim from the map.
func requiredStringClaim(claims jwt.MapClaims, key string) (string, error) {
	raw, ok := claims[key]
	if !ok {
		return "", fmt.Errorf("missing %s claim", key)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s claim is not a string", key)
	}
	return s, nil
}

// requiredFloatClaim extracts a required numeric claim from the map.
func requiredFloatClaim(claims jwt.MapClaims, key string) (float64, error) {
	raw, ok := claims[key]
	if !ok {
		return 0, fmt.Errorf("missing %s claim", key)
	}
	f, ok := raw.(float64)
	if !ok {
		return 0, fmt.Errorf("%s claim is not a number", key)
	}
	return f, nil
}

// audienceClaim extracts the audience claim, handling both string and array formats
// (JWT libraries may represent "aud" as either).
func audienceClaim(claims jwt.MapClaims) (string, error) {
	raw, ok := claims["aud"]
	if !ok {
		return "", fmt.Errorf("missing aud claim")
	}
	switch v := raw.(type) {
	case string:
		return v, nil
	case []interface{}:
		if len(v) == 1 {
			if s, ok := v[0].(string); ok {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("invalid aud claim format")
}
