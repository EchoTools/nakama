package server

import "github.com/go-redis/redis"

type VRMLCache struct {
	redisClient *redis.Client
	prefix      string
}

func NewVRMLCache(redisClient *redis.Client, keyPrefix string) *VRMLCache {
	if keyPrefix == "" {
		keyPrefix = "VRMLCache:"
	}
	if keyPrefix[len(keyPrefix)-1] != ':' {
		keyPrefix += ":"
	}

	return &VRMLCache{
		redisClient: redisClient,
		prefix:      keyPrefix,
	}
}

func (c *VRMLCache) Get(url string) (string, bool, error) {
	v := c.redisClient.Get(c.prefix + url)

	switch v.Err() {
	case nil:
	case redis.Nil:
		return "", false, nil
	default:
		return "", false, v.Err()
	}

	return v.Val(), true, nil
}

func (c *VRMLCache) Set(url string, value string) error {
	return c.redisClient.Set(c.prefix+url, value, 0).Err()
}
