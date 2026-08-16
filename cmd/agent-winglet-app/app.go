package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/umitkaanusta/agent-winglet/internal/appauth"
	"github.com/umitkaanusta/agent-winglet/internal/appipc"
	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/entitlement"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
)

// App is the Wails-bound backend. Every method here does the same thing the
// rest of agent-winglet already does: read the JSON files the hooks and
// install.sh already write, no daemon, no IPC beyond Wails' own JS<->Go
// bridge. See internal/stats' package doc for why lifetime tallies (unlike
// ledger/phase/retire state) are safe to read outside the hooks' own
// same-session lifecycle.
type App struct {
	ctx         context.Context
	ipcListener net.Listener
}

func NewApp() *App {
	return &App{}
}

// GetCompactNudgesEnabled and SetCompactNudgesEnabled expose
// config.Config.CompactNudgeDisabled (inverted, so the Settings screen's
// toggle can talk in on/off terms without re-deriving the double negative).
// The compact nudge itself is a systemMessage/additionalContext emitted
// directly by the hook binaries — this dashboard has no part in showing it,
// only in this one preference for turning it off.
func (a *App) GetCompactNudgesEnabled() bool {
	cfg, err := config.Load()
	if err != nil {
		return true
	}
	return !cfg.CompactNudgeDisabled
}

func (a *App) SetCompactNudgesEnabled(enabled bool) error {
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{Quiet: true}
	}
	cfg.CompactNudgeDisabled = !enabled
	return config.Save(cfg)
}

func (a *App) GetSiteBaseURL() string {
	return appauth.SiteBaseURL()
}

func (a *App) GetAccountStatus() appauth.Status {
	return (&appauth.Client{}).AccountStatus()
}

// StartBrowserSignIn opens the system browser to agentwinglet.com/app-signin
// (Google or magic link, the user's choice there) and blocks until that
// completes and hands a signed-in session back over a loopback redirect, or
// until it times out. See appauth.BrowserSignIn's doc comment for why this
// can't happen inside the app's own webview.
func (a *App) StartBrowserSignIn() (appauth.Status, error) {
	signIn, err := (&appauth.Client{}).PrepareBrowserSignIn(appauth.DeviceInfo{
		Platform: goruntime.GOOS,
	})
	if err != nil {
		return appauth.Status{}, err
	}
	if a.ctx != nil {
		wailsruntime.BrowserOpenURL(a.ctx, signIn.URL)
	}
	return signIn.Await()
}

func (a *App) RefreshEntitlement() (appauth.Status, error) {
	return (&appauth.Client{}).Refresh()
}

// StartFreeTrial claims the signed-in account's one-time 3-day cardless
// trial — see appauth.Client.StartTrial's doc comment for why this never
// touches Subscription.
func (a *App) StartFreeTrial() (appauth.Status, error) {
	return (&appauth.Client{}).StartTrial()
}

func (a *App) Logout() (appauth.Status, error) {
	if err := appauth.Logout(); err != nil {
		return appauth.Status{}, err
	}
	return (&appauth.Client{}).AccountStatus(), nil
}

func (a *App) OpenPricing() {
	if a.ctx != nil {
		wailsruntime.BrowserOpenURL(a.ctx, appauth.SiteBaseURL()+"/#pricing")
	}
}

func (a *App) OpenBillingPortal() {
	a.OpenPricing()
}

func (a *App) SetClaudeHookEnabled(enabled bool) (HookHealth, error) {
	if enabled {
		if err := requireEntitlement(entitlement.FeatureHookSavings); err != nil {
			return HookHealth{}, err
		}
	}
	if err := setGlobalHookEnabled("claude-hook", enabled); err != nil {
		return HookHealth{}, err
	}
	return a.GetHookHealth()
}

func (a *App) SetCodexHookEnabled(enabled bool) (HookHealth, error) {
	if enabled {
		if err := requireEntitlement(entitlement.FeatureHookSavings); err != nil {
			return HookHealth{}, err
		}
	}
	if err := setGlobalHookEnabled("codex-hook", enabled); err != nil {
		return HookHealth{}, err
	}
	return a.GetHookHealth()
}

func requireEntitlement(feature string) error {
	result := entitlement.Check(feature, time.Now())
	if result.Allowed {
		return nil
	}
	switch result.Reason {
	case entitlement.ReasonInactive, entitlement.ReasonExpired, entitlement.ReasonWrongFeature:
		return fmt.Errorf("subscribe to Winglet Pro to use this feature")
	default:
		return fmt.Errorf("sign in to Winglet to use this feature")
	}
}

// HookHealth is a dashboard-facing install/status check for hook setup. Codex
// does not expose a stable machine-readable trust API, so ReviewLikely is a
// conservative symptom check: the hook is configured, but Winglet has not seen
// Codex stats from a session at or after the current hook config timestamp.
type HookHealth struct {
	ClaudeConfigured   bool   `json:"claudeConfigured"`
	ClaudeObserved     bool   `json:"claudeObserved"`
	ClaudeReviewLikely bool   `json:"claudeReviewLikely"`
	ClaudeStatus       string `json:"claudeStatus"`
	ClaudeDetail       string `json:"claudeDetail"`
	ClaudeAction       string `json:"claudeAction"`
	CodexConfigured    bool   `json:"codexConfigured"`
	CodexObserved      bool   `json:"codexObserved"`
	CodexReviewLikely  bool   `json:"codexReviewLikely"`
	CodexStatus        string `json:"codexStatus"`
	CodexDetail        string `json:"codexDetail"`
	CodexAction        string `json:"codexAction"`
}

func (a *App) GetHookHealth() (HookHealth, error) {
	claudeConfigured, claudeConfigTime, err := claudeHookConfigured()
	if err != nil {
		return HookHealth{}, err
	}
	claudeObserved, claudeObservedTime, err := latestAgentSession(stats.AgentClaudeCode)
	if err != nil {
		return HookHealth{}, err
	}
	codexConfigured, codexConfigTime, err := codexHookConfigured()
	if err != nil {
		return HookHealth{}, err
	}
	codexObserved, codexObservedTime, err := latestAgentSession(stats.AgentCodex)
	if err != nil {
		return HookHealth{}, err
	}

	h := HookHealth{
		ClaudeConfigured: claudeConfigured,
		ClaudeObserved:   claudeObserved,
		CodexConfigured:  codexConfigured,
		CodexObserved:    codexObserved,
	}
	h.ClaudeStatus, h.ClaudeDetail, h.ClaudeAction, h.ClaudeReviewLikely = agentHookStatus(
		"Claude Code",
		"claude-hook",
		claudeConfigured,
		claudeConfigTime,
		claudeObserved,
		claudeObservedTime,
	)
	h.CodexStatus, h.CodexDetail, h.CodexAction, h.CodexReviewLikely = agentHookStatus(
		"Codex",
		"codex-hook",
		codexConfigured,
		codexConfigTime,
		codexObserved,
		codexObservedTime,
	)
	return h, nil
}

func agentHookStatus(agentName, binaryName string, configured bool, configTime time.Time, observed bool, observedTime time.Time) (string, string, string, bool) {
	switch {
	case !configured:
		switch binaryName {
		case "claude-hook":
			return "Inactive",
				"Claude Code isn't connected to Winglet yet.",
				"",
				false
		case "codex-hook":
			return "Inactive",
				"Codex isn't connected to Winglet yet.",
				"Click Enable, then in Codex open Settings > Hooks and trust all. Restart and start a new session after.",
				false
		}
	case !observed || observedTime.Before(configTime):
		if binaryName == "codex-hook" {
			return "Extra steps needed",
				"Codex requires manual approval before a new integration can run.",
				"In Codex, open Settings > Hooks and trust all. Restart and start a new session after.",
				true
		}
		return "Waiting for activity",
			fmt.Sprintf("Restart %s and start a new session to activate this.", agentName),
			"",
			false
	default:
		return "Active",
			"Integration complete.",
			"",
			false
	}
	return "Unknown", "Winglet could not classify this hook state.", "", false
}

func claudeHookConfigured() (bool, time.Time, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, time.Time{}, err
	}
	paths := []string{filepath.Join(home, ".claude", "settings.json")}
	return hookConfigured("claude-hook", paths, filepath.Join(".claude", "settings.json"))
}

func codexHookConfigured() (bool, time.Time, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, time.Time{}, err
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	paths := []string{filepath.Join(codexHome, "hooks.json")}
	return hookConfigured("codex-hook", paths, filepath.Join(".codex", "hooks.json"))
}

func setGlobalHookEnabled(binaryName string, enabled bool) error {
	path, err := globalHookConfigPath(binaryName)
	if err != nil {
		return err
	}
	var hookPath string
	if enabled {
		hookPath, err = installedHookPath(binaryName)
		if err != nil {
			return err
		}
	}

	root, err := readJSONObject(path)
	if err != nil {
		return err
	}
	removeHookEntries(root, binaryName)
	if enabled {
		mergeHookEntries(root, binaryName, hookPath)
	}
	return writeJSONObject(path, root)
}

func globalHookConfigPath(binaryName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch binaryName {
	case "claude-hook":
		return filepath.Join(home, ".claude", "settings.json"), nil
	case "codex-hook":
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return filepath.Join(codexHome, "hooks.json"), nil
	default:
		return "", fmt.Errorf("unknown hook binary %q", binaryName)
	}
}

func installedHookPath(binaryName string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	candidates := []string{
		filepath.Join(home, "go", "bin", binaryName),
	}
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		candidates = append(candidates, filepath.Join(gobin, binaryName))
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		candidates = append(candidates, filepath.Join(gopath, "bin", binaryName))
	}
	if lookedUp, err := exec.LookPath(binaryName); err == nil {
		candidates = append(candidates, lookedUp)
	}
	if goPath, err := exec.LookPath("go"); err == nil {
		if out, err := exec.Command(goPath, "env", "GOBIN").Output(); err == nil {
			if gobin := stringTrimSpace(out); gobin != "" {
				candidates = append(candidates, filepath.Join(gobin, binaryName))
			}
		}
		if out, err := exec.Command(goPath, "env", "GOPATH").Output(); err == nil {
			if gopath := stringTrimSpace(out); gopath != "" {
				candidates = append(candidates, filepath.Join(gopath, "bin", binaryName))
			}
		}
	}

	seen := map[string]bool{}
	for _, path := range candidates {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s is not installed. Run ./install.sh once, then try again.", binaryName)
}

func readJSONObject(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]interface{}{}, nil
	}
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if root == nil {
		root = map[string]interface{}{}
	}
	return root, nil
}

func writeJSONObject(path string, root map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agent-winglet-hooks-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
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

func removeHookEntries(root map[string]interface{}, binaryName string) {
	hooks, ok := root["hooks"].(map[string]interface{})
	if !ok {
		return
	}
	for event, rawMatchers := range hooks {
		matchers, ok := rawMatchers.([]interface{})
		if !ok {
			continue
		}
		keptMatchers := make([]interface{}, 0, len(matchers))
		for _, rawMatcher := range matchers {
			matcher, ok := rawMatcher.(map[string]interface{})
			if !ok {
				keptMatchers = append(keptMatchers, rawMatcher)
				continue
			}
			rawHookList, ok := matcher["hooks"].([]interface{})
			if !ok {
				keptMatchers = append(keptMatchers, rawMatcher)
				continue
			}
			keptHooks := make([]interface{}, 0, len(rawHookList))
			for _, rawHook := range rawHookList {
				hook, ok := rawHook.(map[string]interface{})
				if !ok {
					keptHooks = append(keptHooks, rawHook)
					continue
				}
				command, _ := hook["command"].(string)
				if filepath.Base(command) != binaryName {
					keptHooks = append(keptHooks, rawHook)
				}
			}
			if len(keptHooks) == 0 {
				continue
			}
			matcher["hooks"] = keptHooks
			keptMatchers = append(keptMatchers, matcher)
		}
		if len(keptMatchers) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = keptMatchers
		}
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	}
}

func mergeHookEntries(root map[string]interface{}, binaryName, hookPath string) {
	hooks, ok := root["hooks"].(map[string]interface{})
	if !ok {
		hooks = map[string]interface{}{}
		root["hooks"] = hooks
	}
	for _, event := range []string{"PostToolUse", "SessionStart", "PostCompact", "Stop", "SubagentStart", "SubagentStop", "SessionEnd"} {
		entry := map[string]interface{}{
			"hooks": []interface{}{hookCommandEntry(binaryName, hookPath, event)},
		}
		if binaryName == "codex-hook" && event == "PostToolUse" {
			entry["matcher"] = ""
		}
		hooks[event] = appendMatcher(hooks[event], entry)
	}
}

// Codex clamps a SessionEnd hook's timeout to 3s and logs a "clamping
// SessionEnd hook timeout" warning if it's configured any higher — write 3s
// directly so a fresh install doesn't trip that warning.
func hookCommandEntry(binaryName, hookPath, event string) map[string]interface{} {
	entry := map[string]interface{}{
		"type":    "command",
		"command": hookPath,
	}
	if binaryName == "codex-hook" {
		timeout := 5
		if event == "SessionEnd" {
			timeout = 3
		}
		entry["timeout"] = float64(timeout)
	}
	return entry
}

func appendMatcher(raw interface{}, entry map[string]interface{}) []interface{} {
	if matchers, ok := raw.([]interface{}); ok {
		return append(matchers, entry)
	}
	return []interface{}{entry}
}

func stringTrimSpace(data []byte) string {
	start, end := 0, len(data)
	for start < end && (data[start] == ' ' || data[start] == '\n' || data[start] == '\t' || data[start] == '\r') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\t' || data[end-1] == '\r') {
		end--
	}
	return string(data[start:end])
}

func hookConfigured(binaryName string, paths []string, projectRelativePath string) (bool, time.Time, error) {
	dirs, err := registry.Load()
	if err != nil {
		return false, time.Time{}, err
	}
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(dir, projectRelativePath))
	}

	var latest time.Time
	for _, path := range paths {
		if !hookFileContainsCommand(path, binaryName) {
			continue
		}
		if info, err := os.Stat(path); err == nil && info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return !latest.IsZero(), latest, nil
}

func latestAgentSession(agent string) (bool, time.Time, error) {
	dirs, err := registry.Load()
	if err != nil {
		return false, time.Time{}, err
	}

	var latest time.Time
	for _, dir := range dirs {
		files, err := stats.ListSessions(dir)
		if err != nil {
			return false, time.Time{}, err
		}
		for _, f := range files {
			s, err := stats.LoadSession(dir, f.ID)
			if err != nil {
				return false, time.Time{}, err
			}
			if s.Agent == agent && f.ModTime.After(latest) {
				latest = f.ModTime
			}
		}
	}
	return !latest.IsZero(), latest, nil
}

func hookFileContainsCommand(path, binaryName string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	return containsCommand(v, binaryName)
}

func containsCommand(v interface{}, binaryName string) bool {
	switch node := v.(type) {
	case map[string]interface{}:
		if cmd, ok := node["command"].(string); ok && filepath.Base(cmd) == binaryName {
			return true
		}
		for _, child := range node {
			if containsCommand(child, binaryName) {
				return true
			}
		}
	case []interface{}:
		for _, child := range node {
			if containsCommand(child, binaryName) {
				return true
			}
		}
	}
	return false
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.serveIPC()
	go a.ensureTrayRunning()

	// Defensive fallback: install.sh already calls RegisterLoginItem via
	// `--register-login-item` right after installing, but this covers any
	// other way the app ends up running (a dev build, a manual copy) — it's
	// idempotent (see loginitem_darwin.go), so calling it again here is
	// harmless. Errors are logged, not surfaced: the dashboard is fully
	// usable without a registered login item, just without the
	// launch-at-login convenience.
	go func() {
		if err := RegisterLoginItem(); err != nil {
			fmt.Println("agent-winglet-app: login item registration failed:", err)
		}
	}()
}

// ensureTrayRunning launches the tray helper if none is currently reachable
// — the dashboard's side of the same self-healing relaunch the tray already
// does for the dashboard (see cmd/agent-winglet-tray's openDashboard).
// Without this, the tray's own "Quit" also quits a running dashboard (see
// quitDashboard's doc comment there), which leaves no tray around to relaunch
// it — opening the dashboard again (e.g. from Spotlight/the Dock) would
// otherwise never bring the tray icon back on its own, only the next login
// would. Best-effort: a failure here just means no tray icon this session,
// not a broken dashboard, so errors are logged, not surfaced.
func (a *App) ensureTrayRunning() {
	if appipc.TrayRunning() {
		return
	}

	exe, err := trayExecutablePath()
	if err != nil {
		fmt.Println("agent-winglet-app: don't know how to launch the tray helper:", err)
		return
	}
	if err := exec.Command(exe).Start(); err != nil {
		fmt.Println("agent-winglet-app: failed to launch the tray helper at", exe, "-", err)
	}
}

// serveIPC accepts local connections from the tray helper for the lifetime
// of the dashboard. A failure to bind the listener (e.g. two dashboard
// instances racing to claim the port file) is logged, not fatal — the
// dashboard still works standalone, just unreachable from the tray until
// it's restarted.
func (a *App) serveIPC() {
	ln, err := appipc.Listen()
	if err != nil {
		fmt.Println("agent-winglet-app: IPC listener failed to start:", err)
		return
	}
	a.ipcListener = ln

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go a.handleIPCConn(conn)
	}
}

func (a *App) handleIPCConn(conn net.Conn) {
	defer conn.Close()

	cmd, err := appipc.ReadCommand(conn)
	if err != nil {
		return
	}

	switch cmd {
	case appipc.Show:
		wailsruntime.WindowShow(a.ctx)
		wailsruntime.WindowUnminimise(a.ctx)
	case appipc.Quit:
		wailsruntime.Quit(a.ctx)
	}
}

// beforeClose implements options.App.OnBeforeClose: closing the window —
// the titlebar button, Cmd+Q/Alt+F4, or the Dock icon's Quit — always exits
// the dashboard for real. It never touches a tray helper, which (if
// running) is left alone: its "Open Winglet" menu item relaunches the
// dashboard on demand (see cmd/agent-winglet-tray's openDashboard), the same
// way it would for a dashboard that was never started this session.
func (a *App) beforeClose(ctx context.Context) bool {
	return false
}

// shutdown implements options.App.OnShutdown: releases the IPC listener and
// the port file it published, so a stale one left behind doesn't make the
// tray think a dashboard is still running when it isn't.
func (a *App) shutdown(ctx context.Context) {
	if a.ipcListener != nil {
		a.ipcListener.Close()
	}
	appipc.Cleanup()
}

// GetPlatform returns the Go-side runtime.GOOS ("darwin", "windows", or
// "linux") so the frontend can scope OS-specific chrome (the sidebar's
// traffic-light inset padding, mac-only vibrancy) via a data-os attribute
// instead of sniffing the user agent, which WebView2/WebKitGTK/WKWebView
// don't distinguish reliably.
func (a *App) GetPlatform() string {
	return goruntime.GOOS
}

// Card is one of the three summary cards on the card row: a label, a
// hover-tooltip explaining how that figure is derived (real measurement vs.
// estimate, and from what), a pre-formatted primary detail string, and an
// optional secondary line shown beneath it (e.g. a percent under a byte
// count, or a fixed caption under the net-gains multiplier).
type Card struct {
	Label     string `json:"label"`
	Tooltip   string `json:"tooltip"`
	Detail    string `json:"detail"`
	Sub       string `json:"sub"`
	Estimated bool   `json:"estimated"`
}

// BarRow is one row of the suppressed-by-mechanism bar list: a
// label, a hover-tooltip explanation, this mechanism's share of processed
// bytes (already computed via stats.PartPercent, so the frontend never
// re-derives it), a fill ratio relative to the largest mechanism in this
// rollup (so the longest bar reads as ~full width instead of three
// near-invisible slivers when total suppression is small), and the raw
// count/unit-noun plus formatted bytes for the row's bottom line — split
// into their own fields so the frontend never has to parse a composed
// Detail string to lay out left/right.
type BarRow struct {
	Label      string  `json:"label"`
	Tooltip    string  `json:"tooltip"`
	Percent    float64 `json:"percent"`
	HasPercent bool    `json:"hasPercent"`
	FillRatio  float64 `json:"fillRatio"`
	CountLabel string  `json:"countLabel"`
	Bytes      int64   `json:"bytes"`
	BytesLabel string  `json:"bytesLabel"`
}

// Overview is the Overview screen's data (and each Projects-row's data —
// same shape, two call sites). Hierarchy, top to bottom:
//
//  1. HeroHeadline — the headline percent-saved figure (e.g. "38% saved"),
//     the primary claim, restated directly underneath as HeroUsageDetail —
//     the same stretch multiplier reframed as plan stretch, so the hero
//     doesn't repeat itself with a bare "Ax" multiplier right below a
//     percent figure.
//  2. Cards — three small summary cards: bytes suppressed, the same bytes
//     converted to a token estimate, and that token estimate priced in
//     dollars. The net-gains multiplier lives only in HeroUsageDetail now —
//     a fourth card restating it would just repeat the hero line.
//  3. Bars — one row per suppression mechanism, descending by bytes.
type Overview struct {
	HeroBytes           int64   `json:"heroBytes"`
	HeroTotalBytes      int64   `json:"heroTotalBytes"`
	HeroTotalBytesLabel string  `json:"heroTotalBytesLabel"`
	HeroPercent         float64 `json:"heroPercent"`
	HeroHeadline        string  `json:"heroHeadline"`
	HeroUsageDetail     string  `json:"heroUsageDetail"`
	HeroUsageSub        string  `json:"heroUsageSub"`
	HasTranscriptData   bool    `json:"hasTranscriptData"`
	// HasActivity is true the moment any mechanism (dedup/budget-trim/retire)
	// has fired, independent of HasTranscriptData. Suppressed-byte totals
	// need nothing but the hook's own live-written stats file; the percent-
	// saved figure additionally needs this session's transcript, which is
	// only read once, at SessionEnd (see internal/transcript and
	// cmd/claude-hook's handleSessionEnd). Without this split, a session
	// that's still running always renders identically to a session that
	// never did anything — real, live, moment-to-moment suppression data was
	// being discarded for the entire lifetime of every in-progress session.
	HasActivity     bool     `json:"hasActivity"`
	BytesSavedCard  Card     `json:"bytesSavedCard"`
	TokensSavedCard Card     `json:"tokensSavedCard"`
	DollarSavedCard Card     `json:"dollarSavedCard"`
	Bars            []BarRow `json:"bars"`
	ProjectCount    int      `json:"projectCount"`
	SessionCount    int      `json:"sessionCount"`
}

// GetOverview sums every project's session files (see stats.SumProject)
// across every project in the registry that still exists on disk — a live,
// still-in-progress session's file counts the same as a finished one, so
// this screen moves as soon as a session's stats file changes, not only once
// a session ends. A project with no state dir yet (hook installed but never
// fired here) contributes a zero rollup, not an error. There is no
// separately stored overall total: this is always the live sum of what's on
// disk, so it can't drift from what GetProjects/GetSessionStats show for the
// same projects.
func (a *App) GetOverview() (Overview, error) {
	if err := requireEntitlement(entitlement.FeatureDesktopDashboard); err != nil {
		return Overview{}, err
	}
	dirs, err := registry.Load()
	if err != nil {
		return Overview{}, err
	}

	var total overviewTotals
	sessions := 0
	for _, dir := range dirs {
		r, err := stats.SumProject(dir)
		if err != nil {
			return Overview{}, err
		}
		total.add(totalsFromRollup(r))
		sessions += r.Sessions
	}

	return buildOverview(total, len(dirs), sessions), nil
}

// overviewTotals is the minimal common shape buildOverview needs — both
// stats.Session (one session's tally) and stats.Rollup (a project or
// cross-project sum) share these fields, just without a common Go type, so
// callers adapt each into this before formatting.
type overviewTotals struct {
	DedupHits          int
	DedupBytes         int64
	BudgetTrims        int
	BudgetLinesOmitted int
	BudgetBytesOmitted int64
	RetiredCalls       int
	RetiredBytes       int64

	TranscriptTokens       int64
	TranscriptCostUSD      float64
	TranscriptContentBytes int64

	// TokensSaved and DollarSaved are literal sums of each underlying
	// session's own stats.Session.TokensSaved()/DollarSaved() — see those
	// methods' doc comments for why buildOverview must use these summed
	// values instead of re-deriving tokens/dollars saved from this totals'
	// own aggregate percentage.
	TokensSaved float64
	DollarSaved float64
}

// add folds o into t in place, so a caller summing across many projects (or
// many sessions) can do so without a Lifetime/Session type of its own.
func (t *overviewTotals) add(o overviewTotals) {
	t.DedupHits += o.DedupHits
	t.DedupBytes += o.DedupBytes
	t.BudgetTrims += o.BudgetTrims
	t.BudgetLinesOmitted += o.BudgetLinesOmitted
	t.BudgetBytesOmitted += o.BudgetBytesOmitted
	t.RetiredCalls += o.RetiredCalls
	t.RetiredBytes += o.RetiredBytes
	t.TranscriptTokens += o.TranscriptTokens
	t.TranscriptCostUSD += o.TranscriptCostUSD
	t.TranscriptContentBytes += o.TranscriptContentBytes
	t.TokensSaved += o.TokensSaved
	t.DollarSaved += o.DollarSaved
}

func totalsFromRollup(r stats.Rollup) overviewTotals {
	return overviewTotals{
		DedupHits:          r.DedupHits,
		DedupBytes:         r.DedupBytes,
		BudgetTrims:        r.BudgetTrims,
		BudgetLinesOmitted: r.BudgetLinesOmitted,
		BudgetBytesOmitted: r.BudgetBytesOmitted,
		RetiredCalls:       r.RetiredCalls,
		RetiredBytes:       r.RetiredBytes,

		TranscriptTokens:       r.TranscriptTokens,
		TranscriptCostUSD:      r.TranscriptCostUSD,
		TranscriptContentBytes: r.TranscriptContentBytes,

		TokensSaved: r.TokensSaved,
		DollarSaved: r.DollarSaved,
	}
}

func totalsFromSession(s *stats.Session) overviewTotals {
	tokensSaved, _ := s.TokensSaved()
	dollarSaved, _ := s.DollarSaved()
	return overviewTotals{
		DedupHits:          s.DedupHits,
		DedupBytes:         s.DedupBytes,
		BudgetTrims:        s.BudgetTrims,
		BudgetLinesOmitted: s.BudgetLinesOmitted,
		BudgetBytesOmitted: s.BudgetBytesOmitted,
		RetiredCalls:       s.RetiredCalls,
		RetiredBytes:       s.RetiredBytes,

		TranscriptTokens:       s.TranscriptTokens,
		TranscriptCostUSD:      s.TranscriptCostUSD,
		TranscriptContentBytes: s.TranscriptContentBytes,

		TokensSaved: tokensSaved,
		DollarSaved: dollarSaved,
	}
}

func overviewFromSession(s *stats.Session, projectCount, sessionCount int) Overview {
	return buildOverview(totalsFromSession(s), projectCount, sessionCount)
}

// mechanism bundles one suppression mechanism's raw fields for barRows to
// sort and format uniformly, instead of repeating the same five-argument
// shape three times inline.
type mechanism struct {
	label      string
	tooltip    string
	bytes      int64
	countLabel string
}

const (
	dedupTooltip = "When an agent re-runs a shell command it's already run with identical output this session, " +
		"agent-winglet replaces the repeat with a short reference instead of sending it to the model again."
	budgetTrimTooltip = "Commands that succeed but print more than 500 tokens (AgentDiet recommendation) have " +
		"their middle section collapsed to a head/tail summary."
	retireTooltip = "Once a session moves from investigating to editing, earlier read/search/fetch output is " +
		"assumed to have served its purpose and is archived instead of replayed."

	bytesSavedTooltip = "This is a real measurement, not an estimate. It is the size of the tool output " +
		"(command results, file reads, search results) that Winglet removed from the model's context."
	tokensSavedTooltip = "This is an estimate. It applies the percent saved above to the real token count " +
		"from the transcript."
	moneySavedTooltip = "Based on official API usage rates for the model in this session. Cache-read tokens " +
		"and output tokens are excluded."
)

// barRows builds the suppressed-by-mechanism bar list: one row per
// mechanism, descending by bytes, fill width relative to the largest
// mechanism in t (not an absolute 0-100% of total bytes) — otherwise a
// session dominated by one mechanism would render the other two as
// imperceptible slivers instead of comparable bars.
// A mechanism with zero bytes still gets a row (fill ratio 0) so the list
// always shows all three, in whatever order this rollup's own numbers
// produce. total is the same real total buildOverview passes to
// stats.Percent (transcriptContentBytes + suppressed), so each row's
// percent is directly comparable to — and sums to — the hero figure.
func barRows(t overviewTotals, total int64) []BarRow {
	mechanisms := []mechanism{
		{"Repeat output skipped", dedupTooltip, t.DedupBytes,
			fmt.Sprintf("%d hit%s", t.DedupHits, plural(t.DedupHits))},
		{"Long output trimmed", budgetTrimTooltip, t.BudgetBytesOmitted,
			fmt.Sprintf("%d trim%s", t.BudgetTrims, plural(t.BudgetTrims))},
		{"Old investigation output archived", retireTooltip, t.RetiredBytes,
			fmt.Sprintf("%d call%s", t.RetiredCalls, plural(t.RetiredCalls))},
	}
	sort.SliceStable(mechanisms, func(i, j int) bool { return mechanisms[i].bytes > mechanisms[j].bytes })

	var largest int64
	for _, m := range mechanisms {
		if m.bytes > largest {
			largest = m.bytes
		}
	}

	rows := make([]BarRow, len(mechanisms))
	for i, m := range mechanisms {
		pct, ok := stats.PartPercent(m.bytes, total)
		var fillRatio float64
		if largest > 0 {
			fillRatio = float64(m.bytes) / float64(largest)
		}
		rows[i] = BarRow{
			Label:      m.label,
			Tooltip:    m.tooltip,
			Percent:    pct,
			HasPercent: ok,
			FillRatio:  fillRatio,
			CountLabel: m.countLabel,
			Bytes:      m.bytes,
			BytesLabel: formatBytes(m.bytes),
		}
	}
	return rows
}

// buildOverview composes the percent-saved hero, the summary cards, and the
// suppressed-by-mechanism bars from a totals tally.
//
// HeroHeadline is always the headline percent-saved figure, a genuine
// weighted average over t's aggregate bytes (see stats.Percent) — correct to
// compute at any rollup level since it's an intensive quantity, not
// something that should sum across sessions.
//
// The tokens and dollar cards are different: they're extensive quantities
// (a project's tokens/dollars saved is literally the sum of its sessions'),
// so t.TokensSaved/t.DollarSaved must already be sums of each session's own
// stats.Session.TokensSaved()/DollarSaved() (see overviewTotals and
// stats.Rollup) by the time they reach here — buildOverview must not
// re-derive them from t's own aggregate pct and TranscriptTokens, since
// tokens*pct/(100-pct) is a ratio, and a ratio of summed inputs isn't the
// same number as the sum of that ratio computed per session when sessions
// have different suppression densities. TranscriptTokens and
// TranscriptCostUSD (which each session's own TokensSaved/DollarSaved is
// priced from) count only content newly fed to the model — cache-read
// replays and output tokens are excluded at the source — so the rate stays
// a stable per-content-unit price instead of inflating with turn count.
func buildOverview(t overviewTotals, projectCount, sessionCount int) Overview {
	suppressed := t.DedupBytes + t.BudgetBytesOmitted + t.RetiredBytes
	total := t.TranscriptContentBytes + suppressed
	hasActivity := suppressed > 0

	pct, hasPct := stats.Percent(t.DedupBytes, t.BudgetBytesOmitted, t.RetiredBytes, t.TranscriptContentBytes)

	// heroHeadline has three states, not two: real percent (transcript read,
	// at SessionEnd), real-but-partial activity (a mechanism has already
	// fired this session, but the transcript isn't readable yet), or
	// genuinely nothing (a session that hasn't done anything). Collapsing
	// the middle state into "No data yet" is what made a live, in-progress
	// session look identical to an untouched one for its entire duration —
	// see HasActivity's doc comment.
	heroHeadline := "No data yet"
	switch {
	case hasPct:
		heroHeadline = fmt.Sprintf("%.0f%% saved", pct)
	case hasActivity:
		heroHeadline = fmt.Sprintf("%s suppressed so far", formatBytes(suppressed))
	}

	hasTranscriptData := t.TranscriptContentBytes > 0
	hasTokenData := hasTranscriptData && t.TranscriptTokens > 0

	// tokensSavedDetail and dollarDetail use t.TokensSaved/t.DollarSaved —
	// sums of each underlying session's own figure (see overviewTotals'
	// doc comment) — rather than re-deriving from pct and t.TranscriptTokens
	// here, which would reintroduce the ratio-of-sums-vs-sum-of-ratios
	// mismatch between a project's total and the sum of its sessions.
	tokensSavedDetail := "no data yet"
	dollarDetail := "no data yet"
	if hasTokenData {
		tokensSavedDetail = formatTokens(t.TokensSaved)
		dollarDetail = fmt.Sprintf("$%.2f", t.DollarSaved)
	}

	// HeroUsageDetail reframes the percent-saved figure as extra plan
	// stretch — the actual claim agent-winglet makes — expressed as a
	// percent rather than a bare "Ax" multiplier with no unit.
	// stats.Stretch gives that stretch as a ratio of 1 (e.g. 1.6129), so
	// subtracting 100 after scaling to a percent yields "how much more," not
	// "how much total." Tokens/dollars genuinely require the completed
	// transcript (a cost-per-token rate derived from it), so those two cards
	// stay "no data yet" through an in-progress session — that's an honest
	// gap, not the same bug as the headline hiding data it already has.
	heroUsageDetail := "no data yet"
	heroUsageSub := ""
	switch {
	case hasPct:
		extraPercent := stats.Stretch(pct)*100 - 100
		heroUsageDetail = fmt.Sprintf("Your plan goes ~%.0f%% further", extraPercent)
		heroUsageSub = ""
	case hasActivity:
		heroUsageDetail = "% saved lands once this session ends"
	}

	// BytesSavedCard needs nothing but suppressed, which is already known
	// live — it does not need the completed transcript the way tokens/
	// dollars do, so it shouldn't wait for one either.
	bytesSavedDetail := "no data yet"
	if hasActivity {
		bytesSavedDetail = formatBytes(suppressed)
	}

	return Overview{
		HeroBytes:           suppressed,
		HeroTotalBytes:      total,
		HeroTotalBytesLabel: formatBytes(total),
		HeroPercent:         pct,
		HeroHeadline:        heroHeadline,
		HeroUsageDetail:     heroUsageDetail,
		HeroUsageSub:        heroUsageSub,
		HasTranscriptData:   hasTranscriptData,
		HasActivity:         hasActivity,
		BytesSavedCard: Card{
			Label: "Bytes saved", Tooltip: bytesSavedTooltip, Detail: bytesSavedDetail,
			Sub: "Directly measured", Estimated: false,
		},
		TokensSavedCard: Card{
			Label: "Tokens saved", Tooltip: tokensSavedTooltip, Detail: tokensSavedDetail,
			Sub: "Scaled from bytes saved", Estimated: true,
		},
		DollarSavedCard: Card{
			Label: "Money saved", Tooltip: moneySavedTooltip, Detail: dollarDetail,
			Sub: "Uses API pricing", Estimated: true,
		},
		Bars:         barRows(t, total),
		ProjectCount: projectCount,
		SessionCount: sessionCount,
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formatBytes renders a byte count the way the rest of the receipt does —
// a plain magnitude, never implying a cost or token-savings figure, since no
// such validated measurement exists yet.
// formatTokens renders a token count with a K/M/B/T suffix (base 1000, unlike
// formatBytes' base 1024 — tokens aren't a binary-scaled unit). n is a
// float64 because it's a derived estimate (suppressed bytes * a
// tokens-per-byte ratio), not a directly counted integer.
func formatTokens(n float64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%.0f", n)
	}
	div, exp := float64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", n/div, "KMBT"[exp])
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ProjectRow is one row of the Projects screen: the project's name (dir
// basename), whether the hook is actually active for this project right now
// (not just registry presence — see internal/registry's doc comment) via
// either the global ~/.claude/settings.json install.sh wires by default, or
// a legacy/opt-in per-project .claude/settings.json entry, and its lifetime
// tally (Overview) — the same shape the Overview screen uses, so the
// frontend renders both with one shared component.
type ProjectRow struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Installed bool     `json:"installed"`
	Overview  Overview `json:"overview"`
}

// GetProjects returns one row per registered, still-existing project. Each
// row's Overview is that project's sum of session files (see
// stats.SumProject) — the same rollup GetOverview sums across projects, so a
// project row and its slice of Overview can never disagree.
func (a *App) GetProjects() ([]ProjectRow, error) {
	if err := requireEntitlement(entitlement.FeatureDesktopDashboard); err != nil {
		return nil, err
	}
	dirs, err := registry.Load()
	if err != nil {
		return nil, err
	}

	globalInstalled := registry.GlobalHookInstalled()
	rows := make([]ProjectRow, 0, len(dirs))
	for _, dir := range dirs {
		r, err := stats.SumProject(dir)
		if err != nil {
			return nil, err
		}

		rows = append(rows, ProjectRow{
			Name:      filepath.Base(dir),
			Path:      dir,
			Installed: globalInstalled || registry.HookInstalled(dir),
			Overview:  buildOverview(totalsFromRollup(r), 1, r.Sessions),
		})
	}
	return rows, nil
}

// SessionRow is one row of a project's per-session breakdown (ccusage's
// `ccusage session` report shape): one row per still-on-disk
// <sessionID>.stats.json file, using the same percentage-first Overview
// shape as the project/lifetime rollup. This only works because completed
// sessions' stats files are NOT deleted on SessionEnd, and nothing else
// touches or resets one after the fact — a resumed or compacted session
// keeps accumulating onto the same file — so a finished session's file (and
// its suppressed-bytes/transcript-usage tally) persists for this to read.
type SessionRow struct {
	SessionID string   `json:"sessionId"`
	Agent     string   `json:"agent"`
	Overview  Overview `json:"overview"`
}

// GetSessionStats returns one row per session-stats file still on disk for
// projectDir, newest first (see stats.ListSessions).
func (a *App) GetSessionStats(projectDir string) ([]SessionRow, error) {
	if err := requireEntitlement(entitlement.FeatureDesktopDashboard); err != nil {
		return nil, err
	}
	files, err := stats.ListSessions(projectDir)
	if err != nil {
		return nil, err
	}

	rows := make([]SessionRow, 0, len(files))
	for _, f := range files {
		s, err := stats.LoadSession(projectDir, f.ID)
		if err != nil {
			return nil, err
		}
		rows = append(rows, SessionRow{
			SessionID: f.ID,
			Agent:     s.Agent,
			Overview:  overviewFromSession(s, 1, 1),
		})
	}
	return rows, nil
}
