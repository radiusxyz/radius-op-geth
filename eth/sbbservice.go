package eth

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/sbbclient"
)

type Method string

const (
	FinalizeBlock         Method = "finalize_block"
	GetRawTransactionList Method = "get_raw_transaction_list"
)

type SbbService struct {
	sbbClient *sbbclient.SbbClient

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

func (s *SbbService) getRawTransactionList(ctx context.Context, url string) (types.Transactions, error) {
	params := GetRawTransactionListParams{
		RollupId:          "",
		RollupBlockHeight: "",
	}
	body, err := makeBody(GetRawTransactionList, params)
	if err != nil {
		log.Error("failed to make body while finalizing the block in getRawTransactionList method")
		return nil, err
	}

	res := &GetRawTransactionListResponse{}
	if err = s.sbbClient.Send(ctx, url, body, res); err != nil {
		log.Error("failed to send get_transaction_list request to SBB")
		return nil, err
	}

	var txs []*types.Transaction
	for _, hexString := range res.RawTransactionList {
		tx, _ := hexToTx(hexString)
		txs = append(txs, tx)
	}
	return txs, nil
}

func (s *SbbService) finalizeBlock(url string, errCh chan error) {
	if !s.finishedNewPayload {
		errCh <- errors.New("transactions are not ready to be processed yet")
		return
	}

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
		log.Error("failed to make body while finalizing the block in finalizeBlock method")
		errCh <- err
		return
	}
	//0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err = s.sbbClient.Send(ctx, url, body, nil); err != nil {
		log.Error("failed to send finalize_block request to SBB")
		errCh <- err
		return
	}
	s.ResetFinishedNewPayload()
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

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		errCh := make(chan error, 1)

		<-ticker.C
		s.finalizeBlock("", errCh)

		for {
			select {
			case <-ticker.C:
				go s.processTransactions(eventsCtx, "", submitRawTxFn, errCh)
				go s.finalizeBlock("", errCh)
			case <-eventsCtx.Done():
				return nil
			case err, _ := <-errCh:
				log.Error(err.Error())
				// 나중에 0.5초만에 다시 요청하게 하던 스케줄 조정
			}
		}
	})
}

func (s *SbbService) processTransactions(ctx context.Context, url string, submitRawTxFn func(types.Transactions) error, errCh chan error) {
	if s.finishedTxsSetting {
		errCh <- errors.New("transactions are not ready to be processed yet")
		return
	}

	reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
	defer reqCancel()
	txs, err := s.getRawTransactionList(reqCtx, url)
	if err != nil {
		// Error Wrapping 적용해보기.
		log.Error("failed to fetch transactions from SBB")
		errCh <- err
		return
	}
	if err = submitRawTxFn(txs); err != nil {
		log.Error("failed to add the transactions to the transaction pool")
		errCh <- err
		return
	}
	s.CompleteTxsSetting()
}

func hexToTx(str string) (*types.Transaction, error) {
	tx := new(types.Transaction)

	b, err := DecodeHex(str)
	if err != nil {
		return nil, err
	}

	if err = tx.UnmarshalBinary(b); err != nil {
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
