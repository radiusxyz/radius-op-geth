package eth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/ethclient"
	"math/big"
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
	FinalizeBlock          Method = "finalize_block"
	GetRawTransactionList  Method = "get_raw_transaction_list"
	GetSequencerRpcUrlList Method = "get_sequencer_rpc_url_list"
)

type BlockchainService interface {
	SubmitRawTransactions(txs types.Transactions) error
	BlockChain() *core.BlockChain
}

type SbbService struct {
	blockchainService       BlockchainService
	sbbClient               *sbbclient.SbbClient
	ethClient               *ethclient.Client
	platform                string
	rollupId                string
	executorAddress         string
	livenessContractAddress string
	clusterId               string
	seedNodeUrl             string
	finishedTxsSetting      bool
	finishedNewPayload      bool
	noTxPool                bool
	syncTime                uint64
}

func NewSbbService(eth *Ethereum) (*SbbService, error) {
	sbbClient := sbbclient.New()
	ethClient, err := ethclient.Dial(eth.config.L1Url)
	if err != nil {
		return nil, err
	}
	return &SbbService{
		blockchainService:       eth,
		sbbClient:               sbbClient,
		ethClient:               ethClient,
		platform:                eth.config.Platform,
		rollupId:                eth.config.RollupId,
		executorAddress:         eth.config.ExecutorAddress,
		livenessContractAddress: eth.config.LivenessContractAddress,
		clusterId:               eth.config.ClusterId,
		seedNodeUrl:             eth.config.SeedNodeUrl,
		finishedTxsSetting:      false,
		finishedNewPayload:      true,
		noTxPool:                true,
		syncTime:                0,
	}, nil
}

func (s *SbbService) NoTxPool() bool {
	return s.noTxPool
}

func (s *SbbService) SetSyncTime(t uint64) {
	s.syncTime = t
}

func (s *SbbService) IsSyncing() bool {
	return s.syncTime < uint64(time.Now().Unix())+2
	//return true
}

func (s *SbbService) SetNoTxPool(flag bool) {
	s.noTxPool = flag
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

func (s *SbbService) Start() {
	log.Info("Starting sbb service...")
	s.eventLoop()
}

func (s *SbbService) eventLoop() ethereum.Subscription {
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

		errCh := make(chan error, 1)

		for {
			if !s.IsSyncing() {
				break
			}
			time.Sleep(1 * time.Second)
		}

		l1HeadNum, err := s.fetchL1HeadNum(eventsCtx)
		if err != nil {
			return err
		}
		url, err := s.getSequencerUrl(eventsCtx, *l1HeadNum)
		if err != nil {
			return err
		}
		s.finalizeBlock(*url, *l1HeadNum, errCh)

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if s.noTxPool {
					continue
				}
				l1HeadNum, err = s.fetchL1HeadNum(eventsCtx)
				if err != nil {
					log.Error("failed to fetch l1 head", err.Error())
					return err
				}
				url, err = s.getSequencerUrl(eventsCtx, *l1HeadNum)
				if err != nil {
					log.Error("failed to fetch sequencer url", err.Error())
					return err
				}
				go s.processTransactions(eventsCtx, *url, errCh)
				go s.finalizeBlock(*url, *l1HeadNum, errCh)
			case <-eventsCtx.Done():
				return nil
			case err, _ := <-errCh:
				fmt.Println(log.Blue+"something error: ", err.Error())
				// 나중에 0.5초만에 다시 요청하게 하던 스케줄 조정
			}
		}
	})
}

func (s *SbbService) fetchL1HeadNum(ctx context.Context) (*uint64, error) {
	reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
	defer reqCancel()

	l1HeadNum, err := s.ethClient.BlockNumber(reqCtx)
	if err != nil {
		log.Error("failed to fetch l1 head number")
		return nil, err
	}
	return &l1HeadNum, nil
}

func (s *SbbService) getSequencerUrl(ctx context.Context, l1HeadNum uint64) (*string, error) {
	addresses, err := s.fetchSequencerAddressList(ctx, l1HeadNum)
	if err != nil {
		return nil, err
	}
	urls, err := s.fetchSequencerRpcUrlList(ctx, addresses)
	if err != nil {
		return nil, err
	}

	index, err := s.getSequencerIndex(urls)
	if err != nil {
		return nil, err
	}
	return &urls[*index], nil
}

func (s *SbbService) fetchSequencerAddressList(ctx context.Context, l1HeadNum uint64) ([]string, error) {
	reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
	defer reqCancel()

	contractABI, err := abi.JSON(strings.NewReader(abiString))
	if err != nil {
		return nil, err
	}

	METHOD := "getSequencerList"
	contractAddress := common.HexToAddress(s.livenessContractAddress)
	data, err := contractABI.Pack(METHOD, s.clusterId)
	if err != nil {
		return nil, err
	}

	query := ethereum.CallMsg{
		To:   &contractAddress,
		Data: data,
	}
	result, err := s.ethClient.CallContract(reqCtx, query, big.NewInt(int64(l1HeadNum)))
	if err != nil {
		log.Error("failed to make a contract call to retrieve the sequencer URL list.")
		return nil, err
	}
	var sequencerList []common.Address
	err = contractABI.UnpackIntoInterface(&sequencerList, METHOD, result)
	if err != nil {
		return nil, err
	}

	var addresses []string
	for _, addr := range sequencerList {
		if addr != common.HexToAddress("0x0000000000000000000000000000000000000000") {
			addresses = append(addresses, addr.Hex())
		}
	}
	return addresses, nil
}

func (s *SbbService) fetchSequencerRpcUrlList(ctx context.Context, addresses []string) ([]string, error) {
	params := GetSequencerRpcUrlsParams{
		SequencerAddresses: addresses,
	}

	body := newJsonRpcRequest(GetSequencerRpcUrlList, params)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	res := &GetSequencerRpcUrlsResponse{}
	if err := s.sbbClient.Send(ctx, s.seedNodeUrl, body, res); err != nil {
		log.Error("failed to send get_sequencer_rpc_url_list request to seeder node")
		return nil, err
	}
	var urls []string
	for _, sequencer := range res.SequencerRrcUrls {
		urls = append(urls, sequencer.ExternalRpcUrl)
	}
	return urls, nil
}

func (s *SbbService) getSequencerIndex(urls []string) (*uint64, error) {
	blockNum := s.blockchainService.BlockChain().CurrentBlock().Number.Uint64() + 1
	if len(urls) < 1 {
		return nil, errors.New("there are no URLs available, making modular arithmetic impossible")
	}
	mod := blockNum % uint64(len(urls))
	return &mod, nil
}

func (s *SbbService) finalizeBlock(url string, l1HeadNum uint64, errCh chan error) {
	if !s.finishedNewPayload {
		errCh <- errors.New("transactions are not ready to be processed yet")
		return
	}

	targetNum := s.blockchainService.BlockChain().CurrentBlock().Number.Uint64() + 2
	message := FinalizeBlockMessageParams{
		Platform:            s.platform,
		RollupId:            s.rollupId,
		ExecutorAddress:     s.executorAddress,
		PlatformBlockHeight: l1HeadNum,
		RollupBlockHeight:   targetNum,
	}
	params := FinalizeBlockParams{
		Message:   message,
		Signature: "",
	}

	body := newJsonRpcRequest(FinalizeBlock, params)

	//0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.sbbClient.Send(ctx, url, body, nil); err != nil {
		log.Error("failed to send finalize_block request to SBB")
		errCh <- err
		return
	}
	s.ResetFinishedNewPayload()

	fmt.Println(log.Blue+"Successfully finalized the contents to be included in the block. ", "block num: ", targetNum)
}

func (s *SbbService) processTransactions(ctx context.Context, url string, errCh chan error) {
	if s.finishedTxsSetting {
		errCh <- errors.New("transactions are not ready to be processed yet")
		return
	}

	reqCtx, reqCancel := context.WithTimeout(ctx, 5*time.Second)
	defer reqCancel()

	blockNum := s.blockchainService.BlockChain().CurrentBlock().Number.Uint64() + 1
	txs, err := s.getRawTransactionList(reqCtx, url, blockNum)
	if err != nil {
		// Error Wrapping 적용해보기.
		log.Error("failed to fetch transactions from SBB")
		errCh <- err
		return
	}
	if len(txs) > 0 {
		if err = s.blockchainService.SubmitRawTransactions(txs); err != nil {
			log.Error("failed to add the transactions to the transaction pool")
			errCh <- err
			return
		}
	}
	s.CompleteTxsSetting()

	fmt.Println(log.Blue+"Transaction processing succeeded.", "tx count: ", len(txs), " block num: ", blockNum)
}

func (s *SbbService) getRawTransactionList(ctx context.Context, url string, blockNum uint64) (types.Transactions, error) {
	params := GetRawTransactionsParams{
		RollupId:          s.rollupId,
		RollupBlockHeight: blockNum,
	}
	body := newJsonRpcRequest(GetRawTransactionList, params)
	res := &GetRawTransactionsResponse{}
	if err := s.sbbClient.Send(ctx, url, body, res); err != nil {
		log.Error("failed to send get_transaction_list request to SBB")
		return nil, err
	}

	var txs []*types.Transaction
	for _, hexString := range res.RawTransactions {
		tx, _ := hexToTx(hexString)
		txs = append(txs, tx)
	}
	return txs, nil
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

type JSONRPCRequest[T any] struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  T      `json:"params"`
	ID      int    `json:"id"`
}

func newJsonRpcRequest[T any](method Method, params T) JSONRPCRequest[T] {
	return JSONRPCRequest[T]{
		JSONRPC: "2.0",
		Method:  string(method),
		Params:  params,
		ID:      1,
	}
}

type GetSequencerRpcUrlsParams struct {
	SequencerAddresses []string `json:"sequencer_address_list"`
}

type SequencerRpcUrl struct {
	Address        string `json:"address"`
	ExternalRpcUrl string `json:"external_rpc_url"`
	ClusterRpcUrl  string `json:"cluster_rpc_url"`
}

type GetSequencerRpcUrlsResponse struct {
	SequencerRrcUrls []SequencerRpcUrl `json:"sequencer_rpc_url_list"`
}

type FinalizeBlockMessageParams struct {
	Platform            string `json:"platform"`
	RollupId            string `json:"rollup_id"`
	ExecutorAddress     string `json:"executor_address"`
	PlatformBlockHeight uint64 `json:"platform_block_height"`
	RollupBlockHeight   uint64 `json:"rollup_block_height"`
}

type FinalizeBlockParams struct {
	Message   FinalizeBlockMessageParams `json:"message"`
	Signature string                     `json:"signature"`
}

type GetRawTransactionsParams struct {
	RollupId          string `json:"rollup_id"`
	RollupBlockHeight uint64 `json:"rollup_block_height"`
}

type GetRawTransactionsResponse struct {
	RawTransactions []string `json:"raw_transaction_list"`
}

var abiString string = `[
		{
      "inputs": [
        {
          "internalType": "string",
          "name": "clusterId",
          "type": "string"
        }
      ],
      "name": "getSequencerList",
      "outputs": [
        {
          "internalType": "address[]",
          "name": "",
          "type": "address[]"
        }
      ],
      "stateMutability": "view",
      "type": "function"
    }
	]`
