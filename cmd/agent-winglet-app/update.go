package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/AgentWinglet/agent-winglet/internal/buildinfo"
)

const (
	updateCheckURL     = "https://api.github.com/repos/AgentWinglet/agent-winglet/releases/latest"
	updateReleaseURL   = "https://github.com/AgentWinglet/agent-winglet/releases"
	updateCheckTimeout = 6 * time.Second
)

// UpdateStatus is the small dashboard-facing result of checking GitHub's
// latest stable release. Errors intentionally collapse to Available=false so
// startup never shows a scary network failure for a best-effort convenience
// check.
type UpdateStatus struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	ReleaseName    string `json:"releaseName"`
	ReleaseURL     string `json:"releaseUrl"`
	DownloadURL    string `json:"downloadUrl"`
}

type githubRelease struct {
	TagName    string               `json:"tag_name"`
	Name       string               `json:"name"`
	HTMLURL    string               `json:"html_url"`
	Draft      bool                 `json:"draft"`
	Prerelease bool                 `json:"prerelease"`
	Assets     []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// CheckForUpdate checks GitHub Releases once and reports only a newer stable
// release. Dev builds ("dev", or anything that isn't x.y.z) deliberately
// never show an update banner: local builds are not anchored to a release.
func (a *App) CheckForUpdate() UpdateStatus {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	return checkForUpdate(ctx, http.DefaultClient, buildinfo.Version, updateCheckURL)
}

// OpenUpdateRelease opens a GitHub release or release-asset URL returned by
// CheckForUpdate. The URL is constrained to this repository before handing it
// to the OS browser.
func (a *App) OpenUpdateRelease(releaseURL string) {
	if a.ctx == nil || !isAgentWingletReleaseURL(releaseURL) {
		return
	}
	wailsruntime.BrowserOpenURL(a.ctx, releaseURL)
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func checkForUpdate(ctx context.Context, client httpDoer, currentVersion, endpoint string) UpdateStatus {
	status := UpdateStatus{CurrentVersion: currentVersion}
	current, ok := parseStableVersion(currentVersion)
	if !ok {
		return status
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return status
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "AgentWinglet/"+currentVersion)

	resp, err := client.Do(req)
	if err != nil {
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return status
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return status
	}
	if release.Draft || release.Prerelease {
		return status
	}

	latest, ok := parseStableVersion(release.TagName)
	if !ok || !latest.after(current) {
		return status
	}

	status.Available = true
	status.LatestVersion = latest.String()
	status.ReleaseName = release.Name
	status.ReleaseURL = release.HTMLURL
	if status.ReleaseURL == "" {
		status.ReleaseURL = updateReleaseURL + "/tag/" + release.TagName
	}
	status.DownloadURL = releaseDownloadURL(release.Assets, goruntime.GOOS)
	return status
}

func releaseDownloadURL(assets []githubReleaseAsset, goos string) string {
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		if asset.BrowserDownloadURL == "" {
			continue
		}
		switch goos {
		case "darwin":
			if strings.HasSuffix(name, ".dmg") {
				return asset.BrowserDownloadURL
			}
		case "windows":
			if strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".msi") {
				return asset.BrowserDownloadURL
			}
		case "linux":
			if strings.HasSuffix(name, ".deb") || strings.HasSuffix(name, ".appimage") {
				return asset.BrowserDownloadURL
			}
		}
	}
	return ""
}

type semVersion struct {
	major int
	minor int
	patch int
}

func parseStableVersion(value string) (semVersion, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	if value == "" || strings.Contains(value, "-") {
		return semVersion{}, false
	}
	if plus := strings.IndexByte(value, '+'); plus >= 0 {
		value = value[:plus]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return semVersion{}, false
	}
	nums := [3]int{}
	for i, part := range parts {
		if part == "" {
			return semVersion{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return semVersion{}, false
		}
		nums[i] = n
	}
	return semVersion{major: nums[0], minor: nums[1], patch: nums[2]}, true
}

func (v semVersion) after(other semVersion) bool {
	if v.major != other.major {
		return v.major > other.major
	}
	if v.minor != other.minor {
		return v.minor > other.minor
	}
	return v.patch > other.patch
}

func (v semVersion) String() string {
	return strconv.Itoa(v.major) + "." + strconv.Itoa(v.minor) + "." + strconv.Itoa(v.patch)
}

func isAgentWingletReleaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return u.Scheme == "https" &&
		u.Host == "github.com" &&
		(strings.HasPrefix(u.Path, "/AgentWinglet/agent-winglet/releases/tag/") ||
			strings.HasPrefix(u.Path, "/AgentWinglet/agent-winglet/releases/download/") ||
			u.Path == "/AgentWinglet/agent-winglet/releases")
}
