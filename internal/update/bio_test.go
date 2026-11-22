package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewerBioVersions(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"0.4.25-bio.1", "0.4.25-bio.2", true},
		{"0.4.25-bio.2", "0.4.25-bio.2", false},
		{"0.4.25-bio.2", "0.4.25-bio.1", false},
		{"0.4.25-bio.9", "0.4.25-bio.10", true},
		{"0.4.25-bio.3", "0.4.26-bio.1", true},
		{"0.4.26-bio.1", "0.4.25-bio.9", false},
		{"0.4.25", "0.4.25-bio.1", true},
		{"0.4.25-bio.1", "0.4.25", false},
		{"v0.4.25-bio.1", "v0.4.25-bio.2", true},
		{"0.4.25-bio.1", "0.4.25-bio.0", false},
		{"0.4.25-bio.1", "0.4.25-alpha.1", false},
	} {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

func TestLatestBioAtParsesBioReleaseAndPlatformAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			http.Error(w, "missing ua", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{
			"tag_name": "0.4.25-bio.3",
			"html_url": "https://github.com/EnzymeGroup/motty-bio/releases/tag/0.4.25-bio.3",
			"body": "## Motty Bio 0.4.25-bio.3\n- New feature",
			"draft": false,
			"prerelease": false,
			"assets": [
				{"name": "motty-0.4.25-bio.3-darwin-arm64.dmg", "size": 19995682, "browser_download_url": "https://github.com/EnzymeGroup/motty-bio/releases/download/0.4.25-bio.3/motty-0.4.25-bio.3-darwin-arm64.dmg"},
				{"name": "motty-0.4.25-bio.3-windows-amd64.zip", "size": 21967334, "browser_download_url": "https://github.com/EnzymeGroup/motty-bio/releases/download/0.4.25-bio.3/motty-0.4.25-bio.3-windows-amd64.zip"}
			]
		}`))
	}))
	defer server.Close()

	macRel, err := latestBioAt(context.Background(), server.Client(), server.URL, "darwin", "arm64", PackageArchive)
	if err != nil {
		t.Fatalf("latestBioAt darwin: %v", err)
	}
	if macRel.Version != "0.4.25-bio.3" || macRel.URL != "https://github.com/EnzymeGroup/motty-bio/releases/tag/0.4.25-bio.3" {
		t.Fatalf("macRel = %+v", macRel)
	}
	if !macRel.PlatformPackageChecked || !macRel.Asset.Has() || macRel.Asset.Name != "motty-0.4.25-bio.3-darwin-arm64.dmg" {
		t.Fatalf("macRel asset = %+v", macRel.Asset)
	}
	if macRel.Asset.Signed() {
		t.Fatalf("bio asset must not claim upstream selfupdate signature: %+v", macRel.Asset)
	}

	winRel, err := latestBioAt(context.Background(), server.Client(), server.URL, "windows", "amd64", PackagePortable)
	if err != nil {
		t.Fatalf("latestBioAt windows: %v", err)
	}
	if !winRel.PlatformPackageChecked || winRel.Asset.Name != "motty-0.4.25-bio.3-windows-amd64.zip" || winRel.Asset.Signed() {
		t.Fatalf("winRel = %+v", winRel)
	}

	linuxRel, err := latestBioAt(context.Background(), server.Client(), server.URL, "linux", "amd64", PackageArchive)
	if err != nil {
		t.Fatalf("latestBioAt linux: %v", err)
	}
	if !linuxRel.PlatformPackageChecked || linuxRel.Asset.Has() {
		t.Fatalf("linuxRel should report checked but no asset: %+v", linuxRel)
	}
}

func TestLatestBioAtRejectsInvalidAndNotFound(t *testing.T) {
	missing := serve(`{"message":"Not Found"}`, http.StatusNotFound)
	defer missing.Close()
	if _, err := latestBioAt(context.Background(), missing.Client(), missing.URL, "darwin", "arm64", PackageArchive); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("404 gave %v, want ErrNoRelease", err)
	}

	for name, body := range map[string]string{
		"draft":      `{"tag_name":"0.4.25-bio.3","html_url":"https://github.com/EnzymeGroup/motty-bio/releases/tag/0.4.25-bio.3","draft":true}`,
		"prerelease": `{"tag_name":"0.4.25-bio.3","html_url":"https://github.com/EnzymeGroup/motty-bio/releases/tag/0.4.25-bio.3","prerelease":true}`,
		"bad tag":    `{"tag_name":"0.4.25-beta.1","html_url":"https://github.com/EnzymeGroup/motty-bio/releases/tag/0.4.25-beta.1"}`,
		"plain http": `{"tag_name":"0.4.25-bio.3","html_url":"http://example.com/r"}`,
	} {
		srv := serve(body, http.StatusOK)
		if _, err := latestBioAt(context.Background(), srv.Client(), srv.URL, "darwin", "arm64", PackageArchive); err == nil {
			t.Errorf("%s was accepted", name)
		}
		srv.Close()
	}
}
