package logcache

import (
	"sync"

	"github.com/ethereum/go-ethereum/core/types"
)

type Cache struct {
	l *types.Log
	m sync.Mutex
}

func New() *Cache {
	return &Cache{}
}

func (c *Cache) Set(l *types.Log) {
	c.m.Lock()
	c.l = l
	c.m.Unlock()
}

func (c *Cache) Get() *types.Log {
	c.m.Lock()
	defer c.m.Unlock()
	return c.l
}
