package logcache

import (
	"sync"

	"github.com/ethereum/go-ethereum/core/types"
)

type Stats struct {
	FirstBlock                uint64 `json:"first_block"`
	LastBlock                 uint64 `json:"last_block"`
	PriceUpdateCounter        uint64 `json:"price_update_counter"`
	BatchCreatedCounter       uint64 `json:"batch_created_counter"`
	BatchTopUpCounter         uint64 `json:"batch_top_up_counter"`
	BatchDepthIncreaseCounter uint64 `json:"batch_depth_increase_counter"`
}

type Cache struct {
	lastPriceUpdateLog *types.Log
	stats              Stats
	m                  sync.Mutex
}

func New() *Cache {
	return &Cache{
		stats: Stats{},
	}
}

// ProcessLogAndStats updates statistics based on the given log and its type.
// It also caches the log if it's a price update event.
func (c *Cache) ProcessLogAndStats(log types.Log, isPriceUpdate, isBatchCreated, isBatchTopUp, isBatchDepthIncrease bool) {
	c.m.Lock()
	defer c.m.Unlock()

	// Update FirstBlock
	if c.stats.FirstBlock == 0 || log.BlockNumber < c.stats.FirstBlock {
		c.stats.FirstBlock = log.BlockNumber
	}

	// Update LastBlock
	if log.BlockNumber > c.stats.LastBlock {
		c.stats.LastBlock = log.BlockNumber
	}

	// Update counters based on event type
	if isPriceUpdate {
		c.stats.PriceUpdateCounter++
		c.lastPriceUpdateLog = &log // cache the instance of this price update log
	}
	if isBatchCreated {
		c.stats.BatchCreatedCounter++
	}
	if isBatchTopUp {
		c.stats.BatchTopUpCounter++
	}
	if isBatchDepthIncrease {
		c.stats.BatchDepthIncreaseCounter++
	}
}

// GetLastPriceUpdateLog retrieves the last cached PriceUpdate log.
func (c *Cache) GetLastPriceUpdateLog() *types.Log {
	c.m.Lock()
	defer c.m.Unlock()
	return c.lastPriceUpdateLog
}

// GetStats retrieves a copy of the current statistics.
func (c *Cache) GetStats() Stats {
	c.m.Lock()
	defer c.m.Unlock()
	return c.stats
}
