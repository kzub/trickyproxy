package negativecache

import (
	"sync"
	"time"
)

type entry struct {
	expireAt time.Time
}

type Cache struct {
	m   sync.Map
	ttl time.Duration
}

func New(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl}
}

func (c *Cache) Enabled() bool {
	return c.ttl > 0
}

func (c *Cache) Set(key string) {
	if !c.Enabled() {
		return
	}
	c.m.Store(key, entry{expireAt: time.Now().Add(c.ttl)})
}

func (c *Cache) Get(key string) bool {
	v, ok := c.m.Load(key)
	if !ok {
		return false
	}
	e := v.(entry)
	if time.Now().After(e.expireAt) {
		c.m.Delete(key)
		return false
	}
	return true
}
