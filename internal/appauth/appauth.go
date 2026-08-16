// Package appauth owns the desktop app's account/session files and server
// calls. Hook binaries must not import this package because it uses HTTP.
package appauth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/umitkaanusta/agent-winglet/internal/entitlement"
)

const (
	siteBaseURLEnv      = "AGENT_WINGLET_SITE_BASE_URL"
	firebaseAPIKeyEnv   = "AGENT_WINGLET_FIREBASE_API_KEY"
	defaultSiteBaseURL  = "https://agentwinglet.com"
	defaultHTTPTimeout  = 15 * time.Second
	defaultAppVersion   = "dev"
	contentTypeJSON     = "application/json"
	identityToolkitBase = "https://identitytoolkit.googleapis.com/v1"
)

type AuthFile struct {
	SiteBaseURL   string   `json:"siteBaseURL"`
	UIDHash       string   `json:"uidHash"`
	EmailHint     string   `json:"emailHint"`
	DeviceID      string   `json:"deviceId"`
	RefreshToken  string   `json:"refreshToken"`
	LastRefreshAt string   `json:"lastRefreshAt"`
	Account       *Account `json:"account,omitempty"`
}

type Account struct {
	AccountID    string        `json:"accountId"`
	Email        string        `json:"email"`
	Subscription *Subscription `json:"subscription,omitempty"`
}

type Subscription struct {
	Product string `json:"product"`
	Tier    string `json:"tier"`
	Status  string `json:"status"`
	Active  bool   `json:"active"`
}

type Status struct {
	State            string        `json:"state"`
	Message          string        `json:"message"`
	SiteBaseURL      string        `json:"siteBaseURL"`
	EmailHint        string        `json:"emailHint"`
	Subscription     *Subscription `json:"subscription,omitempty"`
	HookAllowed      bool          `json:"hookAllowed"`
	DashboardAllowed bool          `json:"dashboardAllowed"`
	LastRefreshAt    string        `json:"lastRefreshAt"`
	ExpiresAt        string        `json:"expiresAt"`
}

type Client struct {
	HTTPClient *http.Client
}

type DeviceInfo struct {
	DeviceName string `json:"deviceName,omitempty"`
	Platform   string `json:"platform,omitempty"`
	AppVersion string `json:"appVersion,omitempty"`
}

type issueResponse struct {
	RefreshToken string   `json:"refreshToken"`
	Entitlement  string   `json:"entitlement"`
	Account      *Account `json:"account"`
}

type refreshResponse struct {
	Entitlement string   `json:"entitlement"`
	Account     *Account `json:"account"`
}

func SiteBaseURL() string {
	configured := strings.TrimSpace(os.Getenv(siteBaseURLEnv))
	if configured == "" {
		return defaultSiteBaseURL
	}
	return strings.TrimRight(configured, "/")
}

func (c *Client) AccountStatus() Status {
	baseURL := SiteBaseURL()
	auth, err := LoadAuth()
	if err != nil {
		return Status{
			State:       "signed_out",
			Message:     "Sign in to Winglet to enable hook savings.",
			SiteBaseURL: baseURL,
		}
	}
	status := Status{
		State:         "signed_out",
		Message:       "Sign in to Winglet to enable hook savings.",
		SiteBaseURL:   auth.SiteBaseURL,
		EmailHint:     auth.EmailHint,
		LastRefreshAt: auth.LastRefreshAt,
	}
	if auth.Account != nil {
		status.Subscription = auth.Account.Subscription
	}
	hook := entitlement.Check(entitlement.FeatureHookSavings, time.Now())
	dashboard := entitlement.Check(entitlement.FeatureDesktopDashboard, time.Now())
	status.HookAllowed = hook.Allowed
	status.DashboardAllowed = dashboard.Allowed
	if hook.Claims != nil {
		status.ExpiresAt = time.Unix(hook.Claims.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	if status.HookAllowed && status.DashboardAllowed {
		status.State = "subscribed"
		status.Message = "Winglet Pro is active."
		return status
	}
	if hook.Reason == entitlement.ReasonInactive || hook.Reason == entitlement.ReasonExpired || hook.Reason == entitlement.ReasonWrongFeature {
		status.State = "expired"
		status.Message = "Subscribe to Winglet Pro to enable hook savings."
		return status
	}
	if hook.Reason == entitlement.ReasonMissingKey || hook.Reason == entitlement.ReasonInvalid {
		status.State = "server_error"
		status.Message = "Winglet could not verify the local entitlement. Sync again after configuring the entitlement public key."
		return status
	}
	status.State = "signed_out"
	status.Message = "Sign in to Winglet to enable hook savings."
	return status
}

func (c *Client) CompleteFirebaseSignIn(idToken string, info DeviceInfo) (Status, error) {
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return Status{}, fmt.Errorf("Firebase ID token is required")
	}
	deviceID, err := existingOrNewDeviceID()
	if err != nil {
		return Status{}, err
	}
	if info.Platform == "" {
		info.Platform = runtime.GOOS
	}
	if info.AppVersion == "" {
		info.AppVersion = defaultAppVersion
	}
	payload := map[string]string{
		"deviceId":   deviceID,
		"deviceName": info.DeviceName,
		"platform":   info.Platform,
		"appVersion": info.AppVersion,
	}
	var out issueResponse
	if err := c.postJSON(SiteBaseURL()+"/api/app/entitlements/issue", payload, idToken, &out); err != nil {
		return Status{}, err
	}
	if out.RefreshToken == "" || out.Entitlement == "" {
		return Status{}, fmt.Errorf("site returned an incomplete entitlement response")
	}
	auth := AuthFile{
		SiteBaseURL:   SiteBaseURL(),
		DeviceID:      deviceID,
		RefreshToken:  out.RefreshToken,
		LastRefreshAt: time.Now().UTC().Format(time.RFC3339),
		Account:       out.Account,
	}
	if out.Account != nil {
		auth.UIDHash = out.Account.AccountID
		auth.EmailHint = maskEmail(out.Account.Email)
	}
	if err := SaveAuthAndEntitlement(auth, out.Entitlement); err != nil {
		return Status{}, err
	}
	return c.AccountStatus(), nil
}

func (c *Client) SignInWithEmailPassword(email, password string, info DeviceInfo) (Status, error) {
	apiKey := strings.TrimSpace(os.Getenv(firebaseAPIKeyEnv))
	if apiKey == "" {
		return Status{}, fmt.Errorf("%s is required for in-app email/password sign-in", firebaseAPIKeyEnv)
	}
	payload := map[string]interface{}{
		"email":             strings.TrimSpace(email),
		"password":          password,
		"returnSecureToken": true,
	}
	var out struct {
		IDToken string `json:"idToken"`
	}
	url := identityToolkitBase + "/accounts:signInWithPassword?key=" + apiKey
	if err := c.postJSON(url, payload, "", &out); err != nil {
		return Status{}, err
	}
	return c.CompleteFirebaseSignIn(out.IDToken, info)
}

func (c *Client) Refresh() (Status, error) {
	auth, err := LoadAuth()
	if err != nil {
		return c.AccountStatus(), err
	}
	if auth.SiteBaseURL == "" {
		auth.SiteBaseURL = SiteBaseURL()
	}
	payload := map[string]string{
		"deviceId":     auth.DeviceID,
		"refreshToken": auth.RefreshToken,
	}
	var out refreshResponse
	if err := c.postJSON(auth.SiteBaseURL+"/api/app/entitlements/refresh", payload, "", &out); err != nil {
		return c.AccountStatus(), err
	}
	if out.Entitlement == "" {
		return c.AccountStatus(), fmt.Errorf("site returned an incomplete refresh response")
	}
	auth.LastRefreshAt = time.Now().UTC().Format(time.RFC3339)
	auth.Account = out.Account
	if out.Account != nil {
		auth.UIDHash = out.Account.AccountID
		auth.EmailHint = maskEmail(out.Account.Email)
	}
	if err := SaveAuthAndEntitlement(auth, out.Entitlement); err != nil {
		return c.AccountStatus(), err
	}
	return c.AccountStatus(), nil
}

func Logout() error {
	authPath, err := entitlement.AuthPath()
	if err != nil {
		return err
	}
	entPath, err := entitlement.EntitlementPath()
	if err != nil {
		return err
	}
	if err := removeIfExists(authPath); err != nil {
		return err
	}
	return removeIfExists(entPath)
}

func LoadAuth() (AuthFile, error) {
	path, err := entitlement.AuthPath()
	if err != nil {
		return AuthFile{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return AuthFile{}, err
	}
	var auth AuthFile
	if err := json.Unmarshal(data, &auth); err != nil {
		return AuthFile{}, err
	}
	if auth.SiteBaseURL == "" {
		auth.SiteBaseURL = SiteBaseURL()
	}
	return auth, nil
}

func SaveAuthAndEntitlement(auth AuthFile, token string) error {
	root, err := entitlement.RootDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := writeJSON0600(filepath.Join(root, "auth.json"), auth); err != nil {
		return err
	}
	return writeFile0600(filepath.Join(root, "entitlement.jws"), []byte(strings.TrimSpace(token)+"\n"))
}

func (c *Client) postJSON(url string, payload interface{}, bearer string, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentTypeJSON)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var errorBody struct {
			Error interface{} `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&errorBody)
		if msg, ok := errorBody.Error.(string); ok && msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return fmt.Errorf("request failed with HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func existingOrNewDeviceID() (string, error) {
	if auth, err := LoadAuth(); err == nil && strings.TrimSpace(auth.DeviceID) != "" {
		return auth.DeviceID, nil
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "dev_" + hex.EncodeToString(b[:]), nil
}

func writeJSON0600(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile0600(path, append(data, '\n'))
}

func writeFile0600(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-winglet-auth-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func maskEmail(email string) string {
	email = strings.TrimSpace(email)
	parts := strings.Split(email, "@")
	if len(parts) != 2 || parts[0] == "" {
		return email
	}
	local := []rune(parts[0])
	if len(local) <= 1 {
		return "*@" + parts[1]
	}
	return string(local[0]) + "***@" + parts[1]
}
