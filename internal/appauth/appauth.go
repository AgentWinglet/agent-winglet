// Package appauth owns the desktop app's account/session files and server
// calls. Hook binaries must not import this package because it uses HTTP.
package appauth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/umitkaanusta/agent-winglet/internal/entitlement"
)

const (
	siteBaseURL          = "https://agentwinglet.com"
	defaultHTTPTimeout   = 15 * time.Second
	defaultAppVersion    = "dev"
	contentTypeJSON      = "application/json"
	browserSignInTimeout = 3 * time.Minute
	revokeRequestTimeout = 5 * time.Second
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
	Trial        *Trial        `json:"trial,omitempty"`
}

type Subscription struct {
	Product string `json:"product"`
	Tier    string `json:"tier"`
	Status  string `json:"status"`
	Active  bool   `json:"active"`
}

// Trial is the account's cardless-trial eligibility, tracked by the site
// separately from Subscription (see StartTrial's doc comment) — Eligible is
// false both before any trial has ever started and forever after one has
// been used, so the app never needs to distinguish those two cases itself.
type Trial struct {
	Eligible bool `json:"eligible"`
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
	TrialEligible    bool          `json:"trialEligible"`
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

type startTrialResponse struct {
	Entitlement string   `json:"entitlement"`
	Account     *Account `json:"account"`
}

// SiteBaseURL is always the production site. There is no local/dev override:
// the app and hooks only ever talk to agentwinglet.com.
func SiteBaseURL() string {
	return siteBaseURL
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
		status.TrialEligible = auth.Account.Trial != nil && auth.Account.Trial.Eligible
	}
	hook := entitlement.Check(entitlement.FeatureHookSavings, time.Now())
	dashboard := entitlement.Check(entitlement.FeatureDesktopDashboard, time.Now())
	status.HookAllowed = hook.Allowed
	status.DashboardAllowed = dashboard.Allowed
	if hook.Claims != nil {
		status.ExpiresAt = time.Unix(hook.Claims.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	if status.HookAllowed && status.DashboardAllowed {
		if hook.Claims != nil && hook.Claims.Status == "trialing" {
			status.State = "trialing"
			status.Message = "Your 3-day free trial is active."
			return status
		}
		status.State = "subscribed"
		status.Message = "Winglet Pro is active."
		return status
	}
	if hook.Reason == entitlement.ReasonInactive || hook.Reason == entitlement.ReasonExpired || hook.Reason == entitlement.ReasonWrongFeature {
		if status.TrialEligible {
			status.State = "trial_available"
			status.Message = "Start your 3-day free trial to unlock Winglet Pro."
			return status
		}
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

// BrowserSignIn is one in-flight attempt to sign in through the system
// browser. Google and Firebase magic-link sign-in are both blocked from
// running inside the app's own embedded webview (Google's
// disallowed_useragent policy applies to any embedded webview, not just a
// specific auth method), so sign-in happens on agentwinglet.com/app-signin
// in the user's real browser, and hands control back to the app via a
// loopback redirect — the same pattern `gh auth login`/`gcloud auth login`
// use. See SPEC.md's "Desktop App Work" section.
type BrowserSignIn struct {
	// URL is the agentwinglet.com/app-signin address to open in the system
	// browser. The caller (app.go, which holds the Wails context) is
	// responsible for actually opening it.
	URL string

	listener net.Listener
	state    string
	device   DeviceInfo
	client   *Client
}

// PrepareBrowserSignIn starts a loopback listener on an OS-assigned port and
// builds the browser URL for it. Call Await immediately after opening URL in
// the browser; the listener stays open (and unused ports held) until Await
// returns or times out.
func (c *Client) PrepareBrowserSignIn(info DeviceInfo) (*BrowserSignIn, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	state, err := randomHex(16)
	if err != nil {
		listener.Close()
		return nil, err
	}
	if info.Platform == "" {
		info.Platform = runtime.GOOS
	}
	if info.AppVersion == "" {
		info.AppVersion = defaultAppVersion
	}

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	signInURL := SiteBaseURL() + "/app-signin?redirect_uri=" + url.QueryEscape(redirectURI) + "&state=" + url.QueryEscape(state)

	return &BrowserSignIn{
		URL:      signInURL,
		listener: listener,
		state:    state,
		device:   info,
		client:   c,
	}, nil
}

// Await blocks until the browser tab redirects back with a sign-in code, the
// state doesn't match (a stray or forged request hit the loopback port), or
// browserSignInTimeout elapses — matching the ~2 minute TTL the site puts on
// its sign-in codes, plus headroom for the user to actually complete sign-in.
func (b *BrowserSignIn) Await() (Status, error) {
	defer b.listener.Close()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("state") != b.state {
			http.Error(w, "This sign-in link doesn't match the request Winglet made. Close this tab and try again from the app.", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("sign-in state did not match"):
			default:
			}
			return
		}
		code := strings.TrimSpace(query.Get("code"))
		if code == "" {
			http.Error(w, "Winglet did not receive a sign-in code from this link.", http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("site did not return a sign-in code"):
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><body style="font:16px system-ui;padding:2rem">You're signed in. You can close this tab and return to Winglet.</body></html>`)
		select {
		case codeCh <- code:
		default:
		}
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(b.listener) }()
	defer server.Close()

	select {
	case code := <-codeCh:
		return b.client.completeBrowserSignIn(code, b.device)
	case err := <-errCh:
		return Status{}, err
	case <-time.After(browserSignInTimeout):
		return Status{}, fmt.Errorf("sign-in timed out waiting for the browser")
	}
}

func (c *Client) completeBrowserSignIn(code string, info DeviceInfo) (Status, error) {
	deviceID, err := existingOrNewDeviceID()
	if err != nil {
		return Status{}, err
	}
	payload := map[string]string{
		"code":       code,
		"deviceId":   deviceID,
		"deviceName": info.DeviceName,
		"platform":   info.Platform,
		"appVersion": info.AppVersion,
	}
	var out issueResponse
	if err := c.postJSON(SiteBaseURL()+"/api/app/auth/exchange", payload, "", &out); err != nil {
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

// StartTrial claims the account's one-time 3-day cardless trial. The site
// tracks eligibility on the user's own record, separate from Subscription —
// this never creates or touches a Paddle subscription, since Paddle does not
// yet fully support cardless trials — and grants only the first time a given
// account calls it; a repeat call returns an error instead of extending or
// re-granting. On success the site responds the same way Refresh does: a
// freshly signed entitlement whose Claims.Status is "trialing" and whose
// ExpiresAt is now+72h, which internal/entitlement.Authorize already treats
// exactly like an active subscription, so no other app-side gating changes.
func (c *Client) StartTrial() (Status, error) {
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
	var out startTrialResponse
	if err := c.postJSON(auth.SiteBaseURL+"/api/app/trial/start", payload, "", &out); err != nil {
		return c.AccountStatus(), err
	}
	if out.Entitlement == "" {
		return c.AccountStatus(), fmt.Errorf("site returned an incomplete trial response")
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

// Logout always clears local state, even if the site can't be reached —
// signing out must work offline (see SPEC.md's "must not gate" list). The
// server-side revoke is attempted first, best-effort, so a stolen refresh
// token from a wiped device stops working; a failure there doesn't stop the
// local sign-out.
func Logout() error {
	if auth, err := LoadAuth(); err == nil {
		revokeRemote(auth)
	}

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

func revokeRemote(auth AuthFile) {
	if strings.TrimSpace(auth.RefreshToken) == "" || strings.TrimSpace(auth.DeviceID) == "" {
		return
	}
	payload := map[string]string{
		"refreshToken": auth.RefreshToken,
		"deviceId":     auth.DeviceID,
	}
	client := &Client{HTTPClient: &http.Client{Timeout: revokeRequestTimeout}}
	var out struct{}
	_ = client.postJSON(SiteBaseURL()+"/api/app/entitlements/revoke", payload, "", &out)
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

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
