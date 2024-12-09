package eth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/sbbclient"
	"strings"
	"time"
)

type Method string

const (
	FinalizeBlock         Method = "finalize_block"
	GetRawTransactionList Method = "get_raw_transaction_list"
)

type SbbService struct {
	sbbClient          *sbbclient.SbbClient
	finishedTxsSetting bool
	finishedNewPayload bool
}

func (s *SbbService) FinishedTxsSetting() bool {
	return s.finishedTxsSetting
}

func (s *SbbService) FinishedNewPayload() bool {
	return s.finishedNewPayload
}

func (s *SbbService) ResetFinishedTxsSetting() {
	s.finishedTxsSetting = false
}

func (s *SbbService) ResetFinishedNewPayload() {
	s.finishedNewPayload = false
}

func (s *SbbService) CompleteNewPayload() {
	s.finishedNewPayload = true
}

func (s *SbbService) CompleteTxsSetting() {
	s.finishedTxsSetting = true
}

func NewSbbService() (*SbbService, error) {
	client := sbbclient.New()
	return &SbbService{
		sbbClient:          client,
		finishedTxsSetting: false,
		finishedNewPayload: false,
	}, nil
}

func (s *SbbService) Start(submitRawTxFn func(txs types.Transactions) error) {
	s.eventLoop(submitRawTxFn)
}

func makeBody(method Method, params interface{}) ([]byte, error) {
	jsonRpcRequest := newJsonRpcRequest(method, params)
	body, err := json.Marshal(jsonRpcRequest)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (s *SbbService) GetRawTransactionList(ctx context.Context, url string) (types.Transactions, error) {
	params := GetRawTransactionListParams{
		RollupId:          "",
		RollupBlockHeight: "",
	}
	body, err := makeBody(GetRawTransactionList, params)
	if err != nil {
		return nil, err
	}

	res := &GetRawTransactionListResponse{}
	if err = s.sbbClient.Send(ctx, url, body, res); err != nil {
		return nil, err
	}

	var txs []*types.Transaction
	for _, hexString := range res.RawTransactionList {
		tx, _ := hexToTx(hexString)
		txs = append(txs, tx)
	}
	return txs, nil
}

func (s *SbbService) FinalizeBlock(url string) error {
	params := FinalizeBlockParams{
		Platform:            "",
		RollupId:            "",
		BlockCreatorAddress: "",
		ExecutorAddress:     "",
		PlatformBlockHeight: "",
		RollupBlockHeight:   "",
	}
	body, err := makeBody(FinalizeBlock, params)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// TODO: response 타입 알아야함
	if err = s.sbbClient.Send(ctx, url, body, nil); err != nil {
		return err
	}
	return nil
}

func (s *SbbService) finalizeBlockLoop() {

}

func (s *SbbService) eventLoop(submitRawTxFn func(types.Transactions) error) ethereum.Subscription {
	return event.NewSubscription(func(quit <-chan struct{}) error {
		eventsCtx, eventsCancel := context.WithCancel(context.Background())
		defer eventsCancel()
		// We can handle a quit signal while fn is running, by closing the ctx.
		go func() {
			select {
			case <-quit:
				eventsCancel()
			case <-eventsCtx.Done(): // don't wait for quit signal if we closed for other reasons.
				return
			}
		}()

		txsSettingTimer := time.NewTimer(0)
		var txsSettingCh <-chan time.Time
		planAction := func() {
			delay := s.planNextAction()
			txsSettingCh = txsSettingTimer.C
			if len(txsSettingCh) > 0 { // empty if not already drained before resetting
				<-txsSettingCh
			}
			txsSettingTimer.Reset(delay)
		}

		go s.finalizeBlockLoop()

		for {
			planAction()
			select {
			case <-txsSettingCh:
				reqCtx, reqCancel := context.WithTimeout(eventsCtx, 5*time.Second)
				defer reqCancel()
				txs, err := s.GetRawTransactionList(reqCtx, "")
				if err != nil {
					return err
				}
				if err = submitRawTxFn(txs); err != nil {
					return err
				}
			case <-eventsCtx.Done():
				return nil
			}
		}
	})
}

func (s *SbbService) planNextAction() time.Duration {

	return 5
}

func hexToTx(str string) (*types.Transaction, error) {
	tx := new(types.Transaction)

	b, err := DecodeHex(str)
	if err != nil {
		return nil, err
	}

	if err := tx.UnmarshalBinary(b); err != nil {
		return nil, err
	}

	return tx, nil
}

func DecodeHex(str string) ([]byte, error) {
	str = strings.TrimPrefix(str, "0x")

	// Check if the string has an odd length
	if len(str)%2 != 0 {
		// Prepend a '0' to make it even-length
		str = "0" + str
	}

	return hex.DecodeString(str)
}

// JSON-RPC 요청 형식 정의
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      int         `json:"id"`
}

func newJsonRpcRequest(method Method, params interface{}) JSONRPCRequest {
	return JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  string(method),
		Params:  params,
		ID:      1,
	}
}

type FinalizeBlockParams struct {
	Platform            string `json:"platform"`
	RollupId            string `json:"rollup_id"`
	BlockCreatorAddress string `json:"block_creator_address"`
	ExecutorAddress     string `json:"executor_address"`
	PlatformBlockHeight string `json:"platform_block_height"`
	RollupBlockHeight   string `json:"rollup_block_height"`
}

type GetRawTransactionListParams struct {
	RollupId          string `json:"rollup_id"`
	RollupBlockHeight string `json:"rollup_block_height"`
}

type GetRawTransactionListResponse struct {
	RawTransactionList []string `json:"raw_transaction_list"`
}
