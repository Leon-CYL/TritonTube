package web

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

type CachedContentService struct {
	next   VideoContentService
	client *redis.Client
	ttl    time.Duration

	hits   atomic.Uint64
	misses atomic.Uint64
	errors atomic.Uint64
}

type CacheStats struct {
	Hits    uint64  `json:"hits"`
	Misses  uint64  `json:"misses"`
	Errors  uint64  `json:"errors"`
	HitRate float64 `json:"hit_rate"`
}

func NewCachedContentService(
	next VideoContentService,
	client *redis.Client,
	ttl time.Duration,
) *CachedContentService {
	return &CachedContentService{
		next:   next,
		client: client,
		ttl:    ttl,
	}
}

func (c *CachedContentService) Read(videoID, filename string) ([]byte, error) {
	// Keep the first version simple: cache only DASH segments.
	if !strings.HasSuffix(filename, ".m4s") {
		return c.next.Read(videoID, filename)
	}

	ctx := context.Background()
	key := "segment:" + videoID + ":" + filename

	data, err := c.client.Get(ctx, key).Bytes()
	switch {
	case err == nil:
		c.hits.Add(1)
		return data, nil

	case err == redis.Nil:
		c.misses.Add(1)

	default:
		// Redis errors should not prevent video playback.
		c.errors.Add(1)
	}

	data, err = c.next.Read(videoID, filename)
	if err != nil {
		return nil, err
	}

	// A cache write failure should not fail a successful storage read.
	if err := c.client.Set(ctx, key, data, c.ttl).Err(); err != nil {
		c.errors.Add(1)
	}

	return data, nil
}

func (c *CachedContentService) Write(
	videoID,
	filename string,
	data []byte,
) error {
	return c.next.Write(videoID, filename, data)
}

func (c *CachedContentService) WriteBatch(files []ContentFile) (int, error) {
	return c.next.WriteBatch(files)
}

func (c *CachedContentService) Stats() CacheStats {
	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses

	var hitRate float64
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	return CacheStats{
		Hits:    hits,
		Misses:  misses,
		Errors:  c.errors.Load(),
		HitRate: hitRate,
	}
}
