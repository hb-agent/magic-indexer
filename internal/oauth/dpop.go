// Package oauth provides AT Protocol OAuth implementation.
// DPoP (Demonstrating Proof of Possession) implementation per RFC 9449.
package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DPoPKeyPair represents an ES256 key pair for DPoP.
type DPoPKeyPair struct {
	PrivateKey *ecdsa.PrivateKey
	PublicKey  *ecdsa.PublicKey
}

// DPoPValidationResult contains the result of validating a DPoP proof.
type DPoPValidationResult struct {
	JKT string // JWK Thumbprint (SHA-256) of the public key
	JTI string // Unique identifier for replay protection
	IAT int64  // Issued-at timestamp
}

// DPoPClaims represents the claims in a DPoP proof JWT.
type DPoPClaims struct {
	jwt.RegisteredClaims
	HTM   string `json:"htm"`             // HTTP method
	HTU   string `json:"htu"`             // HTTP URI
	ATH   string `json:"ath,omitempty"`   // Access token hash (optional)
	Nonce string `json:"nonce,omitempty"` // Server-provided nonce (optional)
}

// JWK represents a JSON Web Key (subset for ES256 public keys).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	D   string `json:"d,omitempty"` // Private key component (only for private keys)
}

// GenerateDPoPKeyPair generates a new ES256 key pair for DPoP.
func GenerateDPoPKeyPair() (*DPoPKeyPair, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}
	return &DPoPKeyPair{
		PrivateKey: privateKey,
		PublicKey:  &privateKey.PublicKey,
	}, nil
}

// ToJWK converts the key pair to a JWK (public key only).
func (kp *DPoPKeyPair) ToJWK() *JWK {
	return &JWK{
		Kty: "EC",
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(kp.PublicKey.X.Bytes()),
		Y:   base64.RawURLEncoding.EncodeToString(kp.PublicKey.Y.Bytes()),
	}
}

// ToPrivateJWK converts the key pair to a JWK including the private key.
func (kp *DPoPKeyPair) ToPrivateJWK() *JWK {
	jwk := kp.ToJWK()
	jwk.D = base64.RawURLEncoding.EncodeToString(kp.PrivateKey.D.Bytes())
	return jwk
}

// ToJSON returns the JWK as a JSON string (public key only).
func (kp *DPoPKeyPair) ToJSON() (string, error) {
	jwk := kp.ToJWK()
	data, err := json.Marshal(jwk)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ToPrivateJSON returns the JWK as a JSON string including the private key.
func (kp *DPoPKeyPair) ToPrivateJSON() (string, error) {
	jwk := kp.ToPrivateJWK()
	data, err := json.Marshal(jwk)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ParseDPoPKeyPair parses a JWK JSON string into a DPoPKeyPair.
func ParseDPoPKeyPair(jwkJSON string) (*DPoPKeyPair, error) {
	var jwk JWK
	if err := json.Unmarshal([]byte(jwkJSON), &jwk); err != nil {
		return nil, fmt.Errorf("failed to parse JWK: %w", err)
	}

	if jwk.Kty != "EC" || jwk.Crv != "P-256" {
		return nil, errors.New("only EC P-256 keys are supported")
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("failed to decode X: %w", err)
	}

	yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Y: %w", err)
	}

	publicKey := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}

	kp := &DPoPKeyPair{
		PublicKey: publicKey,
	}

	// If private key component is present, parse it
	if jwk.D != "" {
		dBytes, err := base64.RawURLEncoding.DecodeString(jwk.D)
		if err != nil {
			return nil, fmt.Errorf("failed to decode D: %w", err)
		}
		kp.PrivateKey = &ecdsa.PrivateKey{
			PublicKey: *publicKey,
			D:         new(big.Int).SetBytes(dBytes),
		}
	}

	return kp, nil
}

// CalculateJKT calculates the JWK Thumbprint (RFC 7638) for the public key.
func (kp *DPoPKeyPair) CalculateJKT() string {
	return CalculateJKTFromJWK(kp.ToJWK())
}

// CalculateJKTFromJWK calculates the JWK Thumbprint from a JWK.
// Per RFC 7638, for EC keys the thumbprint is SHA-256 of the canonical JSON:
// {"crv":"P-256","kty":"EC","x":"...","y":"..."}
func CalculateJKTFromJWK(jwk *JWK) string {
	// Canonical form requires alphabetical order of members
	//nolint:gocritic // JSON format requires explicit quotes, not %q escaping
	canonical := fmt.Sprintf(`{"crv":"%s","kty":"%s","x":"%s","y":"%s"}`,
		jwk.Crv, jwk.Kty, jwk.X, jwk.Y)
	hash := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// GenerateDPoPProof generates a DPoP proof JWT.
// Parameters:
//   - method: HTTP method (e.g., "POST")
//   - url: HTTP URI being accessed
//   - accessToken: OAuth access token (optional, for ath claim)
//   - nonce: Server-provided nonce (optional)
func (kp *DPoPKeyPair) GenerateDPoPProof(method, url, accessToken, nonce string) (string, error) {
	if kp.PrivateKey == nil {
		return "", errors.New("private key required for proof generation")
	}

	now := time.Now()
	jti, err := generateJTI()
	if err != nil {
		return "", fmt.Errorf("failed to generate JTI: %w", err)
	}

	claims := DPoPClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:       jti,
			IssuedAt: jwt.NewNumericDate(now),
		},
		HTM: method,
		HTU: url,
	}

	// Add access token hash if provided
	if accessToken != "" {
		hash := sha256.Sum256([]byte(accessToken))
		claims.ATH = base64.RawURLEncoding.EncodeToString(hash[:])
	}

	// Add nonce if provided
	if nonce != "" {
		claims.Nonce = nonce
	}

	// Create token with ES256 and JWK header
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)

	// Add typ and jwk to header
	token.Header["typ"] = "dpop+jwt"
	token.Header["jwk"] = kp.ToJWK()

	// Sign the token
	signedToken, err := token.SignedString(kp.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return signedToken, nil
}

// VerifyDPoPProof verifies a DPoP proof JWT.
// Parameters:
//   - proof: The DPoP proof JWT
//   - method: Expected HTTP method
//   - url: Expected HTTP URI
//   - maxAgeSeconds: Maximum allowed age of the proof (typically 300 = 5 minutes)
//
// Returns the validation result or an error.
func VerifyDPoPProof(proof, method, url string, maxAgeSeconds int64) (*DPoPValidationResult, error) {
	// Parse the token without verification first to extract the JWK
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid JWT format")
	}

	// Decode header
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("failed to decode header: %w", err)
	}

	var header struct {
		Typ string `json:"typ"`
		Alg string `json:"alg"`
		JWK *JWK   `json:"jwk"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("failed to parse header: %w", err)
	}

	// Validate header
	if header.Typ != "dpop+jwt" {
		return nil, errors.New("invalid typ: expected dpop+jwt")
	}
	if header.Alg != "ES256" {
		return nil, errors.New("invalid alg: expected ES256")
	}
	if header.JWK == nil {
		return nil, errors.New("missing jwk in header")
	}
	if header.JWK.Kty != "EC" || header.JWK.Crv != "P-256" {
		return nil, errors.New("invalid jwk: expected EC P-256")
	}

	// Parse the public key from JWK
	xBytes, err := base64.RawURLEncoding.DecodeString(header.JWK.X)
	if err != nil {
		return nil, fmt.Errorf("failed to decode X: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(header.JWK.Y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode Y: %w", err)
	}

	publicKey := &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}

	// Now verify the token with the extracted public key
	var claims DPoPClaims
	token, err := jwt.ParseWithClaims(proof, &claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return publicKey, nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to verify token: %w", err)
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}

	// Validate claims
	if claims.HTM != method {
		return nil, fmt.Errorf("method mismatch: expected %s, got %s", method, claims.HTM)
	}
	if claims.HTU != url {
		return nil, fmt.Errorf("url mismatch: expected %s, got %s", url, claims.HTU)
	}
	if claims.ID == "" {
		return nil, errors.New("missing jti claim")
	}
	if claims.IssuedAt == nil {
		return nil, errors.New("missing iat claim")
	}

	// Check age
	iat := claims.IssuedAt.Unix()
	now := time.Now().Unix()
	if now-iat > maxAgeSeconds {
		return nil, fmt.Errorf("proof too old: %d seconds", now-iat)
	}
	// Also check for future-dated proofs (allow 60 second clock skew)
	if iat > now+60 {
		return nil, errors.New("proof issued in the future")
	}

	// Calculate JKT
	jkt := CalculateJKTFromJWK(header.JWK)

	return &DPoPValidationResult{
		JKT: jkt,
		JTI: claims.ID,
		IAT: iat,
	}, nil
}

// VerifyDPoPProofWithATH verifies a DPoP proof JWT and validates the access token hash.
func VerifyDPoPProofWithATH(proof, method, url, accessToken string, maxAgeSeconds int64) (*DPoPValidationResult, error) {
	result, err := VerifyDPoPProof(proof, method, url, maxAgeSeconds)
	if err != nil {
		return nil, err
	}

	// Verify ATH if access token is provided
	if accessToken != "" {
		// Re-parse to get claims (we need ATH)
		token, _ := jwt.ParseWithClaims(proof, &DPoPClaims{}, func(token *jwt.Token) (interface{}, error) {
			return nil, nil // We've already verified, just need to parse
		})
		if claims, ok := token.Claims.(*DPoPClaims); ok {
			expectedATH := sha256.Sum256([]byte(accessToken))
			expectedATHStr := base64.RawURLEncoding.EncodeToString(expectedATH[:])
			if claims.ATH != expectedATHStr {
				return nil, errors.New("access token hash mismatch")
			}
		}
	}

	return result, nil
}

// generateJTI generates a unique identifier for the DPoP proof.
func generateJTI() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
