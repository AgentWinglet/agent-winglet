// Package entitlement verifies the server-issued local entitlement used by the
// app and hook binaries. It deliberately has no networking imports; the Wails
// app refreshes the file, and hooks only verify what is already on disk.
package entitlement

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	Issuer   = "https://agentwinglet.com"
	Audience = "agent-winglet-local"

	FeatureHookSavings      = "hook_savings"
	FeatureDesktopDashboard = "desktop_dashboard"

	AuthNoticeSignedOut = "Winglet is installed but is not signed in. Open Winglet and sign in to enable hook savings."
	AuthNoticeInactive  = "Winglet is installed but your subscription is not active. Open Winglet pricing to subscribe and enable hook savings."
)

type Claims struct {
	Issuer             string   `json:"iss"`
	Audience           string   `json:"aud"`
	Subject            string   `json:"sub"`
	DeviceID           string   `json:"device_id"`
	Plan               string   `json:"plan"`
	Status             string   `json:"status"`
	Features           []string `json:"features"`
	IssuedAt           int64    `json:"issued_at"`
	ExpiresAt          int64    `json:"expires_at"`
	GraceUntil         int64    `json:"grace_until"`
	EntitlementVersion int      `json:"entitlement_version"`
}

type CheckReason string

const (
	ReasonAllowed      CheckReason = "allowed"
	ReasonSignedOut    CheckReason = "signed_out"
	ReasonInactive     CheckReason = "inactive_subscription"
	ReasonInvalid      CheckReason = "invalid_entitlement"
	ReasonExpired      CheckReason = "expired_entitlement"
	ReasonMissingKey   CheckReason = "missing_verification_key"
	ReasonWrongFeature CheckReason = "missing_feature"
)

type CheckResult struct {
	Allowed bool
	Reason  CheckReason
	Message string
	Claims  *Claims
}

type localAuth struct {
	DeviceID string `json:"deviceId"`
}

type jwsHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	KID string `json:"kid"`
}

func AuthPath() (string, error) {
	dir, err := RootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "auth.json"), nil
}

func EntitlementPath() (string, error) {
	dir, err := RootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "entitlement.jws"), nil
}

func RootDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-winglet"), nil
}

func Check(feature string, now time.Time) CheckResult {
	if testAllowUngatedHooks() {
		return CheckResult{Allowed: true, Reason: ReasonAllowed}
	}
	auth, ok := readLocalAuth()
	if !ok || strings.TrimSpace(auth.DeviceID) == "" {
		return denied(ReasonSignedOut)
	}
	path, err := EntitlementPath()
	if err != nil {
		return denied(ReasonSignedOut)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return denied(ReasonSignedOut)
	}
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return denied(ReasonInvalid)
	}
	claims, err := VerifyJWS(strings.TrimSpace(string(data)))
	if err != nil {
		if errors.Is(err, ErrNoVerificationKey) {
			return denied(ReasonMissingKey)
		}
		return denied(ReasonInvalid)
	}
	if claims.DeviceID != auth.DeviceID {
		return denied(ReasonInvalid)
	}
	return Authorize(claims, feature, now)
}

func testAllowUngatedHooks() bool {
	if os.Getenv("AGENT_WINGLET_TEST_ALLOW_UNGATED_HOOKS") != "1" {
		return false
	}
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
}

func Authorize(claims *Claims, feature string, now time.Time) CheckResult {
	if claims == nil {
		return denied(ReasonInvalid)
	}
	if claims.Issuer != Issuer || claims.Audience != Audience || claims.EntitlementVersion != 1 {
		return withClaims(denied(ReasonInvalid), claims)
	}
	if !hasFeature(claims.Features, feature) {
		return withClaims(denied(ReasonWrongFeature), claims)
	}
	nowUnix := now.Unix()
	switch claims.Status {
	case "active", "trialing":
		if nowUnix <= claims.ExpiresAt {
			return CheckResult{Allowed: true, Reason: ReasonAllowed, Claims: claims}
		}
		return withClaims(denied(ReasonExpired), claims)
	case "past_due":
		graceUntil := claims.GraceUntil
		if graceUntil == 0 {
			graceUntil = claims.ExpiresAt
		}
		if nowUnix <= graceUntil {
			return CheckResult{Allowed: true, Reason: ReasonAllowed, Claims: claims}
		}
		return withClaims(denied(ReasonExpired), claims)
	default:
		return withClaims(denied(ReasonInactive), claims)
	}
}

var ErrNoVerificationKey = errors.New("no entitlement verification key configured")

func VerifyJWS(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid compact JWS")
	}
	headerData, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}
	var header jwsHeader
	if err := json.Unmarshal(headerData, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}
	payloadData, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode signature: %w", err)
	}
	if err := verifySignature(header, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, err
	}
	var claims Claims
	if err := json.Unmarshal(payloadData, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}
	return &claims, nil
}

func verifySignature(header jwsHeader, signingInput, sig []byte) error {
	key, ok, err := lookupPublicKey(header.KID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNoVerificationKey
	}
	switch header.Alg {
	case "RS256":
		rsaKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("entitlement key %q is not RSA", header.KID)
		}
		sum := sha256.Sum256(signingInput)
		return rsa.VerifyPKCS1v15(rsaKey, crypto.SHA256, sum[:], sig)
	case "EdDSA":
		edKey, ok := key.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("entitlement key %q is not Ed25519", header.KID)
		}
		if !ed25519.Verify(edKey, signingInput, sig) {
			return fmt.Errorf("invalid Ed25519 entitlement signature")
		}
		return nil
	default:
		return fmt.Errorf("unsupported entitlement alg %q", header.Alg)
	}
}

func lookupPublicKey(kid string) (crypto.PublicKey, bool, error) {
	keys, err := configuredPublicKeys()
	if err != nil {
		return nil, false, err
	}
	if key, ok := keys[kid]; ok {
		return key, true, nil
	}
	if key, ok := keys["*"]; ok {
		return key, true, nil
	}
	return nil, false, nil
}

func configuredPublicKeys() (map[string]crypto.PublicKey, error) {
	keys := map[string]crypto.PublicKey{}
	for kid, pemText := range embeddedPublicKeys {
		key, err := parsePublicKeyPEM(pemText)
		if err != nil {
			return nil, err
		}
		keys[kid] = key
	}
	if raw := strings.TrimSpace(os.Getenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEYS")); raw != "" {
		var envKeys map[string]string
		if err := json.Unmarshal([]byte(raw), &envKeys); err != nil {
			return nil, fmt.Errorf("parse AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEYS: %w", err)
		}
		for kid, pemText := range envKeys {
			key, err := parsePublicKeyPEM(pemText)
			if err != nil {
				return nil, err
			}
			keys[kid] = key
		}
	}
	if pemText := strings.TrimSpace(os.Getenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY")); pemText != "" {
		key, err := parsePublicKeyPEM(pemText)
		if err != nil {
			return nil, err
		}
		kid := strings.TrimSpace(os.Getenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY_ID"))
		if kid == "" {
			kid = "*"
		}
		keys[kid] = key
	}
	return keys, nil
}

func parsePublicKeyPEM(pemText string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(pemText, `\n`, "\n")))
	if block == nil {
		return nil, fmt.Errorf("invalid entitlement public key PEM")
	}
	if block.Type == "CERTIFICATE" {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		return cert.PublicKey, nil
	}
	if block.Type == "RSA PUBLIC KEY" {
		return x509.ParsePKCS1PublicKey(block.Bytes)
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

func NoticeFor(reason CheckReason) string {
	switch reason {
	case ReasonInactive, ReasonExpired, ReasonWrongFeature:
		return AuthNoticeInactive
	default:
		return AuthNoticeSignedOut
	}
}

func ShouldEmitNotice(agent, sessionID string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return true
	}
	dir, err := RootDir()
	if err != nil {
		return true
	}
	sum := sha256.Sum256([]byte(sessionID))
	name := hex.EncodeToString(sum[:]) + ".json"
	path := filepath.Join(dir, "hook-notices", sanitize(agent), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return true
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func denied(reason CheckReason) CheckResult {
	return CheckResult{Reason: reason, Message: NoticeFor(reason)}
}

func withClaims(result CheckResult, claims *Claims) CheckResult {
	result.Claims = claims
	return result
}

func readLocalAuth() (localAuth, bool) {
	path, err := AuthPath()
	if err != nil {
		return localAuth{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return localAuth{}, false
	}
	var auth localAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return localAuth{}, false
	}
	return auth, true
}

func hasFeature(features []string, feature string) bool {
	for _, candidate := range features {
		if candidate == feature {
			return true
		}
	}
	return false
}

func sanitize(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "agent"
	}
	return b.String()
}
