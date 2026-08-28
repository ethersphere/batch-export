package ethclientwrapper

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

func TestFinalizedBlockNumberQueriesFinalizedTag(t *testing.T) {
	t.Parallel()

	var (
		mu        sync.Mutex
		gotMethod string
		gotParams []any
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params []any           `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		gotMethod = req.Method
		gotParams = req.Params
		mu.Unlock()

		header := types.Header{Number: big.NewInt(100), Difficulty: big.NewInt(0)}
		headerJSON, err := json.Marshal(&header)
		if err != nil {
			t.Errorf("marshal header: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, headerJSON)
	}))
	defer server.Close()

	client, err := NewClient(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	got, err := client.FinalizedBlockNumber(context.Background())
	if err != nil {
		t.Fatalf("FinalizedBlockNumber: %v", err)
	}
	if got != 100 {
		t.Errorf("got block %d, want 100", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != "eth_getBlockByNumber" {
		t.Errorf("got method %q, want eth_getBlockByNumber", gotMethod)
	}
	if len(gotParams) != 2 || gotParams[0] != "finalized" || gotParams[1] != false {
		t.Errorf("got params %v, want [finalized false]", gotParams)
	}
}
