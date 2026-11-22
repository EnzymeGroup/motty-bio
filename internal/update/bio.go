package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"motty/internal/buildinfo"
	"motty/internal/netclient"
)

// BioRepo is the GitHub repository that publishes Motty Bio Edition releases.
const BioRepo = "EnzymeGroup/motty-bio"

// BioFeed is the GitHub Releases API endpoint for Motty Bio Edition.
const BioFeed = "https://api.github.com/repos/" + BioRepo + "/releases/latest"

// BioReleasesURL is the human-facing releases page for Motty Bio Edition.
const BioReleasesURL = "https://github.com/" + BioRepo + "/releases"

// CurrentVersion returns the effective version of this build, including any
// derivative VariantSuffix (for example "0.4.25-bio.2").
func CurrentVersion() string {
	return buildinfo.DisplayVersion(Current)
}

func isBioBuild() bool {
	return buildinfo.VariantSuffix != ""
}

// parseBioVersion parses either a plain dotted release ("0.4.25") or a Bio
// derivative release ("0.4.25-bio.2"), with an optional leading "v".
func parseBioVersion(version string) (base [3]int, bio int, ok bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	if head, tail, hasBio := strings.Cut(trimmed, "-bio."); hasBio {
		base, ok = parse(head)
		if !ok {
			return [3]int{}, 0, false
		}
		n, err := strconv.Atoi(tail)
		if err != nil || n <= 0 || n > 1<<20 {
			return [3]int{}, 0, false
		}
		return base, n, true
	}
	base, ok = parse(trimmed)
	return base, 0, ok
}

func newerBioVersion(current, latest string) bool {
	aBase, aBio, okA := parseBioVersion(current)
	bBase, bBio, okB := parseBioVersion(latest)
	if !okA || !okB {
		return false
	}
	for i := range aBase {
		if aBase[i] != bBase[i] {
			return bBase[i] > aBase[i]
		}
	}
	return bBio > aBio
}

// CheckBio checks the Motty Bio GitHub Releases feed exclusively so a Bio build
// never prompts to download or overwrite itself with an upstream Motty release.
func CheckBio(ctx context.Context, client *http.Client, goos, goarch string, pkg Package) (Release, error) {
	attempt, cancel := context.WithTimeout(ctx, feedAttemptTimeout)
	defer cancel()
	return latestBioAt(attempt, client, BioFeed, goos, goarch, pkg)
}

type githubBioAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"browser_download_url"`
}

func latestBioAt(ctx context.Context, client *http.Client, feedURL, goos, goarch string, pkg Package) (Release, error) {
	if client == nil {
		client = netclient.Client(15 * time.Second)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "motty/"+CurrentVersion())
	response, err := client.Do(request)
	if err != nil {
		return Release{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Release{}, ErrNoRelease
	}
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("update: the release feed answered %s", response.Status)
	}
	var body struct {
		Tag        string           `json:"tag_name"`
		URL        string           `json:"html_url"`
		Notes      string           `json:"body"`
		Draft      bool             `json:"draft"`
		Prerelease bool             `json:"prerelease"`
		Assets     []githubBioAsset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxFeedBytes)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("update: reading the release feed: %w", err)
	}
	version := strings.TrimPrefix(strings.TrimSpace(body.Tag), "v")
	if _, _, ok := parseBioVersion(version); !ok || body.Draft || body.Prerelease {
		return Release{}, errors.New("update: the release feed has no usable version")
	}
	if !strings.HasPrefix(body.URL, "https://") {
		return Release{}, errors.New("update: the release page is not an https address")
	}
	release := Release{
		Version: version,
		URL:     body.URL,
		Notes:   cutUTF8(body.Notes, maxNotesBytes),
	}
	if len(body.Assets) > 0 {
		release.PlatformPackageChecked = true
		if matched, ok := matchBioAsset(body.Assets, version, goos, goarch, pkg); ok {
			// Leave Signature empty and FeedVerified false so Bio builds open
			// the Bio GitHub release page instead of attempting upstream
			// Ed25519 selfupdate replacement.
			release.Asset = Asset{
				Name: matched.Name,
				URL:  matched.DownloadURL,
				Size: matched.Size,
			}
		}
	}
	return release, nil
}

func matchBioAsset(assets []githubBioAsset, version, goos, goarch string, pkg Package) (githubBioAsset, bool) {
	candidates := bioAssetNames(version, goos, goarch, pkg)
	for _, want := range candidates {
		for _, a := range assets {
			if a.Name == want && strings.HasPrefix(a.DownloadURL, "https://") && a.Size > 0 && a.Size <= maxAssetBytes {
				return a, true
			}
		}
	}
	return githubBioAsset{}, false
}

func bioAssetNames(version, goos, goarch string, pkg Package) []string {
	switch goos {
	case "darwin":
		if goarch == "arm64" || goarch == "amd64" {
			return []string{
				"motty-" + version + "-darwin-" + goarch + ".dmg",
				"motty-" + version + "-darwin-" + goarch + ".zip",
			}
		}
	case "windows":
		if goarch == "amd64" || goarch == "arm64" {
			if pkg == PackagePortable {
				return []string{
					"motty-" + version + "-windows-" + goarch + ".zip",
					"motty-" + version + "-windows-" + goarch + "-setup.exe",
				}
			}
			return []string{
				"motty-" + version + "-windows-" + goarch + "-setup.exe",
				"motty-" + version + "-windows-" + goarch + ".zip",
			}
		}
	case "linux":
		if goarch == "amd64" || goarch == "arm64" {
			if pkg == PackageDeb {
				return []string{"motty_" + version + "_" + goarch + ".deb"}
			}
			return []string{
				"motty-" + version + "-linux-" + goarch + ".tar.gz",
				"motty_" + version + "_" + goarch + ".deb",
			}
		}
	}
	return nil
}
