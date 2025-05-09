package logcache_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/logcache"
)

func TestGetLastPriceUpdateLog(t *testing.T) {
	t.Parallel()
	c := logcache.New()
	b := types.Log{}
	c.ProcessLogAndStats(b, true, false, false, false)
	if c.GetLastPriceUpdateLog() == nil {
		t.Fatal("expected block to be cached")
	}
}
