//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// updateServiceCacheStub keeps one entry per release source, mirroring the
// per-repo scoping the Redis implementation applies.
type updateServiceCacheStub struct {
	entries map[string]string
}

func (s *updateServiceCacheStub) GetUpdateInfo(_ context.Context, repo string) (string, error) {
	if data, ok := s.entries[repo]; ok && data != "" {
		return data, nil
	}
	return "", errors.New("cache miss")
}

func (s *updateServiceCacheStub) SetUpdateInfo(_ context.Context, repo string, data string, _ time.Duration) error {
	if s.entries == nil {
		s.entries = map[string]string{}
	}
	s.entries[repo] = data
	return nil
}

type updateServiceGitHubClientStub struct {
	release         *GitHubRelease
	recentReleases  []*GitHubRelease
	recentErr       error
	latestRepos     []string
	recentReposList []string
}

func (s *updateServiceGitHubClientStub) FetchLatestRelease(_ context.Context, repo string) (*GitHubRelease, error) {
	s.latestRepos = append(s.latestRepos, repo)
	return s.release, nil
}

func (s *updateServiceGitHubClientStub) FetchRecentReleases(_ context.Context, repo string, _ int) ([]*GitHubRelease, error) {
	s.recentReposList = append(s.recentReposList, repo)
	return s.recentReleases, s.recentErr
}

func (s *updateServiceGitHubClientStub) DownloadFile(context.Context, string, string, int64) error {
	panic("DownloadFile should not be called when no update is available")
}

func (s *updateServiceGitHubClientStub) FetchChecksumFile(context.Context, string) ([]byte, error) {
	panic("FetchChecksumFile should not be called when no update is available")
}

func TestUpdateServicePerformUpdateNoUpdateReturnsSentinel(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{
			release: &GitHubRelease{
				TagName: "v0.1.132",
				Name:    "v0.1.132",
			},
		},
		"0.1.132",
		"release",
	)

	err := svc.PerformUpdate(context.Background(), "")

	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNoUpdateAvailable))
	require.ErrorIs(t, err, ErrNoUpdateAvailable)
}

func newRollbackTestService(current string, releases []*GitHubRelease) *UpdateService {
	return NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentReleases: releases},
		current,
		"release",
	)
}

func TestUpdateServiceListRollbackVersionsFiltersAndCaps(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148", PublishedAt: "2026-07-09T00:00:00Z"},                       // newer than current: excluded
		{TagName: "v0.1.147", PublishedAt: "2026-07-08T00:00:00Z"},                       // current: excluded
		{TagName: "v0.1.146-rc1", PublishedAt: "2026-07-07T12:00:00Z", Prerelease: true}, // prerelease: excluded
		{TagName: "v0.1.146", PublishedAt: "2026-07-07T00:00:00Z"},
		{TagName: "v0.1.145", PublishedAt: "2026-07-06T00:00:00Z", Draft: true}, // draft: excluded
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"},
		{TagName: "v0.1.144", PublishedAt: "2026-07-05T00:00:00Z"}, // duplicate: excluded
		{TagName: "v0.1.143", PublishedAt: "2026-07-04T00:00:00Z"},
		{TagName: "v0.1.142", PublishedAt: "2026-07-03T00:00:00Z"}, // beyond cap of 3: excluded
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background(), "")

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.144", versions[1].Version)
	require.Equal(t, "0.1.143", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsSortsUnorderedInput(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.144"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background(), "")

	require.NoError(t, err)
	require.Len(t, versions, 3)
	require.Equal(t, "0.1.146", versions[0].Version)
	require.Equal(t, "0.1.145", versions[1].Version)
	require.Equal(t, "0.1.144", versions[2].Version)
}

func TestUpdateServiceListRollbackVersionsEmptyWhenNoneOlder(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.148"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	versions, err := svc.ListRollbackVersions(context.Background(), "")

	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestUpdateServiceListRollbackVersionsPropagatesFetchError(t *testing.T) {
	svc := NewUpdateService(
		&updateServiceCacheStub{},
		&updateServiceGitHubClientStub{recentErr: errors.New("github unavailable")},
		"0.1.147",
		"release",
	)

	_, err := svc.ListRollbackVersions(context.Background(), "")

	require.Error(t, err)
	require.Contains(t, err.Error(), "github unavailable")
}

func TestUpdateServiceRollbackToVersionRejectsDisallowedTargets(t *testing.T) {
	releases := []*GitHubRelease{
		{TagName: "v0.1.148"},
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
		{TagName: "v0.1.145"},
		{TagName: "v0.1.144"},
		{TagName: "v0.1.143"},
		{TagName: "v0.1.142"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	for _, target := range []string{
		"",         // empty
		"0.1.147",  // current version
		"v0.1.147", // current version with prefix
		"0.1.148",  // newer than current
		"0.1.142",  // older than the 3 most recent
		"9.9.9",    // nonexistent
	} {
		err := svc.RollbackToVersion(context.Background(), "", target)
		require.ErrorIs(t, err, ErrRollbackVersionNotAllowed, "target %q should be rejected", target)
	}
}

func TestUpdateServiceRollbackToVersionAcceptsVPrefix(t *testing.T) {
	// No platform asset in the release: the target passes the allowlist check
	// and fails later at asset lookup, proving the version itself was accepted.
	releases := []*GitHubRelease{
		{TagName: "v0.1.147"},
		{TagName: "v0.1.146"},
	}
	svc := newRollbackTestService("0.1.147", releases)

	err := svc.RollbackToVersion(context.Background(), "", "v0.1.146")

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRollbackVersionNotAllowed)
	require.Contains(t, err.Error(), "no compatible release found")
}

func TestUpdateServiceSourceSelectionAllowlist(t *testing.T) {
	svc := newRollbackTestService("0.1.147", nil).WithConfiguredRepo("fork-owner/sub2api")

	require.Equal(t, []string{defaultGitHubRepo, "fork-owner/sub2api"}, svc.Sources())

	repo, err := svc.resolveRepo("")
	require.NoError(t, err)
	require.Equal(t, "fork-owner/sub2api", repo, "configured fork becomes the default source")

	repo, err = svc.resolveRepo(defaultGitHubRepo)
	require.NoError(t, err)
	require.Equal(t, defaultGitHubRepo, repo, "upstream stays selectable")

	repo, err = svc.resolveRepo("https://github.com/fork-owner/sub2api/releases/tag/v0.1.176")
	require.NoError(t, err)
	require.Equal(t, "fork-owner/sub2api", repo, "a pasted release URL maps onto the same source")

	for _, rejected := range []string{"someone-else/sub2api", "fork-owner", "a/b/c", "fork-owner/..", "not a repo"} {
		_, err := svc.resolveRepo(rejected)
		require.Error(t, err, "%q must not resolve", rejected)
	}
}

func TestUpdateServiceInvalidConfiguredRepoIsIgnored(t *testing.T) {
	svc := newRollbackTestService("0.1.147", nil).WithConfiguredRepo("https://example.com/evil")

	require.Equal(t, []string{defaultGitHubRepo}, svc.Sources())
	repo, err := svc.resolveRepo("")
	require.NoError(t, err)
	require.Equal(t, defaultGitHubRepo, repo)
}

func TestUpdateServiceCheckUpdateCachesPerSource(t *testing.T) {
	cache := &updateServiceCacheStub{}
	client := &updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.148"}}
	svc := NewUpdateService(cache, client, "0.1.147", "release").WithConfiguredRepo("fork-owner/sub2api")

	forkInfo, err := svc.CheckUpdate(context.Background(), false, "")
	require.NoError(t, err)
	require.Equal(t, "fork-owner/sub2api", forkInfo.Repo)
	require.Equal(t, []string{defaultGitHubRepo, "fork-owner/sub2api"}, forkInfo.AvailableRepos)
	require.True(t, forkInfo.HasUpdate)

	// Switching source must refetch instead of reusing the other source's entry.
	upstreamInfo, err := svc.CheckUpdate(context.Background(), false, defaultGitHubRepo)
	require.NoError(t, err)
	require.Equal(t, defaultGitHubRepo, upstreamInfo.Repo)
	require.Equal(t, []string{"fork-owner/sub2api", defaultGitHubRepo}, client.latestRepos)

	require.Contains(t, cache.entries, "fork-owner/sub2api")
	require.Contains(t, cache.entries, defaultGitHubRepo)

	// Now both sources are warm; a third check stays cached.
	cached, err := svc.CheckUpdate(context.Background(), false, "fork-owner/sub2api")
	require.NoError(t, err)
	require.True(t, cached.Cached)
	require.Len(t, client.latestRepos, 2)
}
