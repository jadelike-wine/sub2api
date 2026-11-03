package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const updateCacheKeyPrefix = "update:latest"

// updateCacheKey scopes the entry per release source, so switching between the
// upstream repository and a configured fork never serves the other source's
// cached version check.
func updateCacheKey(repo string) string {
	if repo == "" {
		return updateCacheKeyPrefix
	}
	return updateCacheKeyPrefix + ":" + repo
}

type updateCache struct {
	rdb *redis.Client
}

func NewUpdateCache(rdb *redis.Client) service.UpdateCache {
	return &updateCache{rdb: rdb}
}

func (c *updateCache) GetUpdateInfo(ctx context.Context, repo string) (string, error) {
	return c.rdb.Get(ctx, updateCacheKey(repo)).Result()
}

func (c *updateCache) SetUpdateInfo(ctx context.Context, repo string, data string, ttl time.Duration) error {
	return c.rdb.Set(ctx, updateCacheKey(repo), data, ttl).Err()
}
