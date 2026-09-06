package authserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidToken is returned for any access token that must not be trusted:
// malformed, wrong algorithm, bad signature, wrong issuer or audience, or
// expired. The reason is never exposed to the caller.
var ErrInvalidToken = errors.New("jeton d'accès invalide")

// jwtHeader is the only header this server mints or accepts. Anything else,
// "none" in particular, is rejected before the signature is even checked.
type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

const (
	algHS256 = "HS256"
	typJWT   = "JWT"
)

// Claims is the payload of an access token. The subject is the tenant id: it
// is what the MCP layer uses to scope every single read and write.
type Claims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Audience  string `json:"aud"`
	ClientID  string `json:"client_id"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	JTI       string `json:"jti"`
}

// TenantID is the tenant this token authorizes.
func (c *Claims) TenantID() string { return c.Subject }

// Expiry is the absolute expiration of the token.
func (c *Claims) Expiry() time.Time { return time.Unix(c.ExpiresAt, 0).UTC() }

// MintAccessToken signs a JWT for a tenant. It returns the compact
// serialization and its expiry.
func (s *Server) MintAccessToken(tenantID, clientID string) (string, time.Time, error) {
	jti, err := randomSecret()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("jti: %w", err)
	}
	now := s.clock.Now()
	expiry := now.Add(s.opts.AccessTokenTTL)
	claims := Claims{
		Issuer:    s.opts.Issuer,
		Subject:   tenantID,
		Audience:  s.opts.Resource,
		ClientID:  clientID,
		IssuedAt:  now.Unix(),
		ExpiresAt: expiry.Unix(),
		JTI:       jti,
	}

	header, err := encodeSegment(jwtHeader{Alg: algHS256, Typ: typJWT})
	if err != nil {
		return "", time.Time{}, err
	}
	payload, err := encodeSegment(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	signingInput := header + "." + payload
	signature := base64.RawURLEncoding.EncodeToString(s.sign(signingInput))
	return signingInput + "." + signature, expiry, nil
}

// VerifyAccessToken checks the signature, the algorithm, the issuer, the
// audience and the expiry of a token, in that order.
func (s *Server) VerifyAccessToken(token string) (*Claims, error) {
	headerSeg, payloadSeg, signatureSeg, ok := splitToken(token)
	if !ok {
		return nil, ErrInvalidToken
	}

	var header jwtHeader
	if err := decodeSegment(headerSeg, &header); err != nil {
		return nil, ErrInvalidToken
	}
	// Reject anything that is not exactly the algorithm we mint: this is the
	// guard against the "alg: none" and the algorithm confusion attacks.
	if header.Alg != algHS256 || (header.Typ != "" && header.Typ != typJWT) {
		return nil, ErrInvalidToken
	}

	signature, err := base64.RawURLEncoding.DecodeString(signatureSeg)
	if err != nil {
		return nil, ErrInvalidToken
	}
	expected := s.sign(headerSeg + "." + payloadSeg)
	if subtle.ConstantTimeCompare(signature, expected) != 1 {
		return nil, ErrInvalidToken
	}

	var claims Claims
	if err := decodeSegment(payloadSeg, &claims); err != nil {
		return nil, ErrInvalidToken
	}
	if claims.Issuer != s.opts.Issuer || claims.Audience != s.opts.Resource {
		return nil, ErrInvalidToken
	}
	if claims.Subject == "" {
		return nil, ErrInvalidToken
	}
	if claims.ExpiresAt == 0 || !s.clock.Now().Before(claims.Expiry()) {
		return nil, ErrInvalidToken
	}
	return &claims, nil
}

func (s *Server) sign(signingInput string) []byte {
	mac := hmac.New(sha256.New, s.opts.SigningKey)
	mac.Write([]byte(signingInput))
	return mac.Sum(nil)
}

// splitToken cuts a compact JWT into its three segments without allocating a
// slice, and rejects anything that is not exactly three segments.
func splitToken(token string) (header, payload, signature string, ok bool) {
	first := strings.IndexByte(token, '.')
	if first < 0 {
		return "", "", "", false
	}
	rest := token[first+1:]
	second := strings.IndexByte(rest, '.')
	if second < 0 {
		return "", "", "", false
	}
	header, payload, signature = token[:first], rest[:second], rest[second+1:]
	if header == "" || payload == "" || signature == "" || strings.ContainsRune(signature, '.') {
		return "", "", "", false
	}
	return header, payload, signature, true
}

func encodeSegment(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode jwt segment: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeSegment(segment string, out any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// hashToken is how refresh tokens are stored: only their SHA-256 ever
// touches the database.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
