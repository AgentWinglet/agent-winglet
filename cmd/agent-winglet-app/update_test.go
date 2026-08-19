package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseStableVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  semVersion
		ok    bool
	}{
		{name: "plain", input: "1.2.3", want: semVersion{major: 1, minor: 2, patch: 3}, ok: true},
		{name: "tag prefix", input: "v1.2.3", want: semVersion{major: 1, minor: 2, patch: 3}, ok: true},
		{name: "build metadata", input: "1.2.3+abc123", want: semVersion{major: 1, minor: 2, patch: 3}, ok: true},
		{name: "dev", input: "dev", ok: false},
		{name: "prerelease", input: "1.2.3-beta.1", ok: false},
		{name: "missing patch", input: "1.2", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseStableVersion(tt.input)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("version = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSemVersionAfter(t *testing.T) {
	current := semVersion{major: 0, minor: 9, patch: 0}
	newer := semVersion{major: 0, minor: 10, patch: 0}
	if !newer.after(current) {
		t.Fatal("0.10.0 should compare newer than 0.9.0")
	}
	if current.after(newer) {
		t.Fatal("0.9.0 should not compare newer than 0.10.0")
	}
}

func TestCheckForUpdateReportsNewerStableRelease(t *testing.T) {
	client := &fakeHTTPClient{
		statusCode: http.StatusOK,
		body: `{
			"tag_name": "v0.10.0",
			"name": "Winglet v0.10.0",
			"html_url": "https://github.com/AgentWinglet/agent-winglet/releases/tag/v0.10.0",
			"draft": false,
			"prerelease": false,
			"assets": [
				{
					"name": "Winglet-0.10.0-macOS-universal.dmg",
					"browser_download_url": "https://github.com/AgentWinglet/agent-winglet/releases/download/v0.10.0/Winglet-0.10.0-macOS-universal.dmg"
				}
			]
		}`,
		checkRequest: func(r *http.Request) {
			if got := r.Header.Get("User-Agent"); got != "AgentWinglet/0.9.0" {
				t.Fatalf("User-Agent = %q", got)
			}
		},
	}

	status := checkForUpdate(context.Background(), client, "0.9.0", updateCheckURL)
	if !status.Available {
		t.Fatal("expected update to be available")
	}
	if status.LatestVersion != "0.10.0" {
		t.Fatalf("LatestVersion = %q, want 0.10.0", status.LatestVersion)
	}
	if status.ReleaseURL == "" {
		t.Fatal("ReleaseURL should be set")
	}
}

func TestReleaseDownloadURLPicksPlatformInstaller(t *testing.T) {
	assets := []githubReleaseAsset{
		{Name: "Winglet-0.10.0-macOS-universal.dmg", BrowserDownloadURL: "https://example.com/winglet.dmg"},
		{Name: "Winglet-0.10.0-windows-amd64.exe", BrowserDownloadURL: "https://example.com/winglet.exe"},
		{Name: "winglet_0.10.0_amd64.deb", BrowserDownloadURL: "https://example.com/winglet.deb"},
	}
	tests := []struct {
		goos string
		want string
	}{
		{goos: "darwin", want: "https://example.com/winglet.dmg"},
		{goos: "windows", want: "https://example.com/winglet.exe"},
		{goos: "linux", want: "https://example.com/winglet.deb"},
		{goos: "freebsd", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			if got := releaseDownloadURL(assets, tt.goos); got != tt.want {
				t.Fatalf("releaseDownloadURL(%q) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}

func TestCheckForUpdateSkipsDevBuildsAndPrereleases(t *testing.T) {
	client := &fakeHTTPClient{
		statusCode: http.StatusOK,
		body:       `{"tag_name":"v0.10.0-beta.1","prerelease":true}`,
	}

	dev := checkForUpdate(context.Background(), client, "dev", updateCheckURL)
	if dev.Available {
		t.Fatal("dev builds should not report updates")
	}
	if client.calls != 0 {
		t.Fatalf("dev build should skip network, got %d request(s)", client.calls)
	}

	prerelease := checkForUpdate(context.Background(), client, "0.9.0", updateCheckURL)
	if prerelease.Available {
		t.Fatal("prereleases should not report updates")
	}
}

func TestIsAgentWingletReleaseURL(t *testing.T) {
	valid := "https://github.com/AgentWinglet/agent-winglet/releases/tag/v0.10.0"
	if !isAgentWingletReleaseURL(valid) {
		t.Fatalf("%s should be accepted", valid)
	}
	download := "https://github.com/AgentWinglet/agent-winglet/releases/download/v0.10.0/Winglet-0.10.0-macOS-universal.dmg"
	if !isAgentWingletReleaseURL(download) {
		t.Fatalf("%s should be accepted", download)
	}
	invalid := "https://github.com/someone/else/releases/tag/v0.10.0"
	if isAgentWingletReleaseURL(invalid) {
		t.Fatalf("%s should be rejected", invalid)
	}
}

type fakeHTTPClient struct {
	statusCode   int
	body         string
	calls        int
	checkRequest func(*http.Request)
}

func (c *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.calls++
	if c.checkRequest != nil {
		c.checkRequest(req)
	}
	return &http.Response{
		StatusCode: c.statusCode,
		Body:       io.NopCloser(strings.NewReader(c.body)),
	}, nil
}
