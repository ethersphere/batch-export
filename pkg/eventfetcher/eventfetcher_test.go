package eventfetcher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethersphere/batch-export/pkg/ethclientwrapper"
	"github.com/ethersphere/batch-export/pkg/eventfetcher"
	"github.com/ethersphere/bee/v2/pkg/log"
)

const testABI = `[
	{"type":"event","name":"BatchCreated","inputs":[]},
	{"type":"event","name":"BatchTopUp","inputs":[]},
	{"type":"event","name":"BatchDepthIncrease","inputs":[]},
	{"type":"event","name":"PriceUpdate","inputs":[]}
]`

type blockRange struct{ from, to uint64 }

// fakeRPC serves eth_blockNumber (latest), eth_getBlockByNumber for the
// finalized tag, and eth_getLogs, recording every queried log range.
func fakeRPC(t *testing.T, latest, finalized uint64, ranges *[]blockRange, mu *sync.Mutex) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params []any           `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}

		var result string
		switch req.Method {
		case "eth_blockNumber":
			result = fmt.Sprintf("%q", hexutil.Uint64(latest).String())
		case "eth_getBlockByNumber":
			header := types.Header{Number: new(big.Int).SetUint64(finalized), Difficulty: big.NewInt(0)}
			headerJSON, err := json.Marshal(&header)
			if err != nil {
				t.Errorf("marshal header: %v", err)
				return
			}
			result = string(headerJSON)
		case "eth_getLogs":
			query, ok := req.Params[0].(map[string]any)
			if !ok {
				t.Errorf("eth_getLogs params[0] is %T, want object", req.Params[0])
				return
			}
			from, err := hexutil.DecodeUint64(query["fromBlock"].(string))
			if err != nil {
				t.Errorf("decode fromBlock: %v", err)
				return
			}
			to, err := hexutil.DecodeUint64(query["toBlock"].(string))
			if err != nil {
				t.Errorf("decode toBlock: %v", err)
				return
			}
			mu.Lock()
			*ranges = append(*ranges, blockRange{from: from, to: to})
			mu.Unlock()
			result = "[]"
		default:
			t.Errorf("unexpected rpc method %q", req.Method)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
}

func TestGetLogsEndsAtFinalizedBlock(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		ranges []blockRange
	)

	server := fakeRPC(t, 200, 100, &ranges, &mu)
	defer server.Close()

	ec, err := ethclientwrapper.NewClient(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer ec.Close()

	contractABI, err := abi.JSON(strings.NewReader(testABI))
	if err != nil {
		t.Fatalf("parse abi: %v", err)
	}

	client := eventfetcher.NewClient(ec, contractABI, 10, log.Noop)

	logChan, errorChan := client.GetLogs(context.Background(), &eventfetcher.Request{
		Address:    common.HexToAddress("0x000000000000000000000000000000000000bEEF"),
		StartBlock: 95,
		EndBlock:   0,
	})

	for range logChan { //nolint:revive // drain until closed
	}
	for err := range errorChan {
		t.Fatalf("GetLogs: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []blockRange{{from: 95, to: 100}}
	if len(ranges) != len(want) || ranges[0] != want[0] {
		t.Errorf("queried ranges %v, want %v (end must be the finalized block, not latest)", ranges, want)
	}
}
