package server

import "github.com/go-redis/redis"

type VRMLCache struct {
	redisClient *redis.Client
	cacheKey    string
}

func NewVRMLCache(redisClient *redis.Client, cacheKey string) *VRMLCache {
	return &VRMLCache{
		redisClient: redisClient,
		cacheKey:    cacheKey,
	}
}

func (c *VRMLCache) Get(key string) (string, bool, error) {
	v := c.redisClient.Get(key)
	if v.Err() != nil {
		if v.Err() == redis.Nil {
			return "", false, nil
		}
		return "", false, v.Err()
	}

	return v.Val(), true, nil
}

func (c *VRMLCache) Set(key string, value string) error {
	return c.redisClient.Set(key, value, 0).Err()
}
