package entitlement

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckAllowsValidRS256Entitlement(t *testing.T) {
	now := time.Unix(1000, 0)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	writeLocalAuth(t, "dev_test")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY_ID", "test-rsa")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY", publicKeyPEM(t, &priv.PublicKey))
	writeEntitlement(t, signRSA(t, priv, "test-rsa", validClaims(now)))

	got := Check(FeatureHookSavings, now)
	if !got.Allowed {
		t.Fatalf("Allowed = false, reason %q", got.Reason)
	}
}

func TestCheckAllowsValidEdDSAEntitlement(t *testing.T) {
	now := time.Unix(1000, 0)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeLocalAuth(t, "dev_test")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY_ID", "test-ed")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY", publicKeyPEM(t, pub))
	writeEntitlement(t, signEdDSA(t, priv, "test-ed", validClaims(now)))

	got := Check(FeatureHookSavings, now)
	if !got.Allowed {
		t.Fatalf("Allowed = false, reason %q", got.Reason)
	}
}

func TestCheckDeniesMissingAuthAsSignedOut(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	got := Check(FeatureHookSavings, time.Unix(1000, 0))
	if got.Allowed || got.Reason != ReasonSignedOut {
		t.Fatalf("Check = %+v, want signed out denial", got)
	}
}

func TestCheckDeniesExpiredEntitlementAsInactiveNotice(t *testing.T) {
	now := time.Unix(1000, 0)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	claims := validClaims(now)
	claims.ExpiresAt = now.Add(-time.Second).Unix()
	writeLocalAuth(t, "dev_test")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY_ID", "test-rsa")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY", publicKeyPEM(t, &priv.PublicKey))
	writeEntitlement(t, signRSA(t, priv, "test-rsa", claims))

	got := Check(FeatureHookSavings, now)
	if got.Allowed || got.Reason != ReasonExpired || NoticeFor(got.Reason) != AuthNoticeInactive {
		t.Fatalf("Check = %+v, want expired subscription denial", got)
	}
}

func TestCheckDeniesBadSignature(t *testing.T) {
	now := time.Unix(1000, 0)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	writeLocalAuth(t, "dev_test")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY_ID", "test-rsa")
	t.Setenv("AGENT_WINGLET_ENTITLEMENT_PUBLIC_KEY", publicKeyPEM(t, &priv.PublicKey))
	token := signRSA(t, priv, "test-rsa", validClaims(now))
	parts := strings.Split(token, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString([]byte("bad-signature"))
	writeEntitlement(t, strings.Join(parts, "."))

	got := Check(FeatureHookSavings, now)
	if got.Allowed || got.Reason != ReasonInvalid {
		t.Fatalf("Check = %+v, want invalid denial", got)
	}
}

func TestAuthorizePastDueGrace(t *testing.T) {
	now := time.Unix(1000, 0)
	claims := validClaims(now)
	claims.Status = "past_due"
	claims.ExpiresAt = now.Add(-time.Hour).Unix()
	claims.GraceUntil = now.Add(time.Hour).Unix()
	if got := Authorize(claims, FeatureHookSavings, now); !got.Allowed {
		t.Fatalf("past due inside grace denied: %+v", got)
	}
	claims.GraceUntil = now.Add(-time.Second).Unix()
	if got := Authorize(claims, FeatureHookSavings, now); got.Allowed || got.Reason != ReasonExpired {
		t.Fatalf("past due outside grace = %+v, want expired", got)
	}
}

func TestAuthorizeDeniedStates(t *testing.T) {
	now := time.Unix(1000, 0)
	claims := validClaims(now)
	claims.Status = "canceled"
	if got := Authorize(claims, FeatureHookSavings, now); got.Allowed || got.Reason != ReasonInactive {
		t.Fatalf("canceled = %+v, want inactive", got)
	}
	claims = validClaims(now)
	claims.Features = nil
	if got := Authorize(claims, FeatureHookSavings, now); got.Allowed || got.Reason != ReasonWrongFeature {
		t.Fatalf("missing feature = %+v, want missing feature", got)
	}
}

func writeLocalAuth(t *testing.T, deviceID string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root, err := RootDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"deviceId":"` + deviceID + `"}`
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeEntitlement(t *testing.T, token string) {
	t.Helper()
	path, err := EntitlementPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
}

func validClaims(now time.Time) *Claims {
	return &Claims{
		Issuer:             Issuer,
		Audience:           Audience,
		Subject:            "sha256:user",
		DeviceID:           "dev_test",
		Plan:               "winglet_pro",
		Status:             "active",
		Features:           []string{FeatureHookSavings, FeatureDesktopDashboard},
		IssuedAt:           now.Unix(),
		ExpiresAt:          now.Add(time.Hour).Unix(),
		GraceUntil:         now.Add(time.Hour).Unix(),
		EntitlementVersion: 1,
	}
}

func signRSA(t *testing.T, priv *rsa.PrivateKey, kid string, claims *Claims) string {
	t.Helper()
	signingInput := signingInput(t, "RS256", kid, claims)
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func signEdDSA(t *testing.T, priv ed25519.PrivateKey, kid string, claims *Claims) string {
	t.Helper()
	signingInput := signingInput(t, "EdDSA", kid, claims)
	sig := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func signingInput(t *testing.T, alg, kid string, claims *Claims) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
}

func publicKeyPEM(t *testing.T, pub interface{}) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := pem.Encode(&b, &pem.Block{Type: "PUBLIC KEY", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
