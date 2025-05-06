package logcache_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/logcache"
)

func TestBasic(t *testing.T) {
	t.Parallel()
	c := logcache.New()
	b := &types.Log{}
	c.Set(b)
	if c.Get() == nil {
		t.Fatal("expected block to be cached")
	}
}
