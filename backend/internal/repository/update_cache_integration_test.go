//go:build integration

package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type UpdateCacheSuite struct {
	IntegrationRedisSuite
	cache *updateCache
}

const (
	upstreamRepo = "Wei-Shaw/sub2api"
	forkRepo     = "example-fork/sub2api"
)

func (s *UpdateCacheSuite) SetupTest() {
	s.IntegrationRedisSuite.SetupTest()
	s.cache = NewUpdateCache(s.rdb).(*updateCache)
}

func (s *UpdateCacheSuite) TestGetUpdateInfo_Missing() {
	_, err := s.cache.GetUpdateInfo(s.ctx, upstreamRepo)
	require.True(s.T(), errors.Is(err, redis.Nil), "expected redis.Nil for missing update info")
}

func (s *UpdateCacheSuite) TestSetAndGetUpdateInfo() {
	updateTTL := 5 * time.Minute
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, upstreamRepo, "v1.2.3", updateTTL), "SetUpdateInfo")

	info, err := s.cache.GetUpdateInfo(s.ctx, upstreamRepo)
	require.NoError(s.T(), err, "GetUpdateInfo")
	require.Equal(s.T(), "v1.2.3", info, "update info mismatch")
}

func (s *UpdateCacheSuite) TestSetUpdateInfo_TTL() {
	updateTTL := 5 * time.Minute
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, upstreamRepo, "v1.2.3", updateTTL))

	ttl, err := s.rdb.TTL(s.ctx, updateCacheKey(upstreamRepo)).Result()
	require.NoError(s.T(), err, "TTL updateCacheKey")
	s.AssertTTLWithin(ttl, 1*time.Second, updateTTL)
}

func (s *UpdateCacheSuite) TestSetUpdateInfo_Overwrite() {
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, upstreamRepo, "v1.0.0", 5*time.Minute))
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, upstreamRepo, "v2.0.0", 5*time.Minute))

	info, err := s.cache.GetUpdateInfo(s.ctx, upstreamRepo)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "v2.0.0", info, "expected overwritten value")
}

// TestSetUpdateInfo_ScopedPerRepo is what lets an admin flip between the upstream
// repository and a configured fork without reading the other source's result.
func (s *UpdateCacheSuite) TestSetUpdateInfo_ScopedPerRepo() {
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, upstreamRepo, "0.1.100", 5*time.Minute))
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, forkRepo, "0.1.200", 5*time.Minute))

	upstream, err := s.cache.GetUpdateInfo(s.ctx, upstreamRepo)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "0.1.100", upstream)

	fork, err := s.cache.GetUpdateInfo(s.ctx, forkRepo)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "0.1.200", fork)
}

func (s *UpdateCacheSuite) TestSetUpdateInfo_ZeroTTL() {
	// TTL=0 means persist forever (no expiry) in Redis SET command
	require.NoError(s.T(), s.cache.SetUpdateInfo(s.ctx, upstreamRepo, "v0.0.0", 0))

	info, err := s.cache.GetUpdateInfo(s.ctx, upstreamRepo)
	require.NoError(s.T(), err)
	require.Equal(s.T(), "v0.0.0", info)

	ttl, err := s.rdb.TTL(s.ctx, updateCacheKey(upstreamRepo)).Result()
	require.NoError(s.T(), err)
	// TTL=-1 means no expiry, TTL=-2 means key doesn't exist
	require.Equal(s.T(), time.Duration(-1), ttl, "expected TTL=-1 for key with no expiry")
}

func TestUpdateCacheSuite(t *testing.T) {
	suite.Run(t, new(UpdateCacheSuite))
}
