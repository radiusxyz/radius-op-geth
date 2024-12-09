package eth

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/sbbclient"
	"io/ioutil"
	"net/http"
	"time"
)

type SbbService struct {
	sbbClient          *sbbclient.SbbClient
	finishedTxsSetting bool
	finishedNewPayload bool
}

func New(ctx context.Context, sbbUrl string) (*SbbService, error) {
	client := sbbclient.New()
	return &SbbService{
		sbbClient:          client,
		finishedTxsSetting: false,
		finishedNewPayload: false,
	}, nil
}

func (s *SbbService) Start() {

}

func makeGetRawTransactionListMessage(method string, rollupId int, rollupBlockHeight int) []byte {

}

func makeFinalizeBlockMessage() []byte {
	message := map[string]interface{}{
		"platform":              "ethereum",
		"rollup_id":             "rollup_id",
		"block_creator_address": "sequencer address",
		"executor_address":      "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		"platform_block_height": 6893,
		"rollup_block_height":   18,
	}

	finalizeBlockRequest := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "finalize_block",
		Params: map[string]interface{}{
			"message":   message,
			"signature": "",
		},
		ID: 1,
	}
	finalizeReqBytes, _ := json.Marshal(finalizeBlockRequest)
	return finalizeReqBytes
}

func requestFinalizeBlockMessage() []byte {

	req, _ := http.NewRequest("POST", "", bytes.NewBuffer(finalize_reqBytes))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "no-cache") // 캐시 방지

	// HTTP 클라이언트 생성 및 요청 전송
	client := &http.Client{
		Timeout: 10 * time.Second, // 타임아웃 10초 설정
		Transport: &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			DisableKeepAlives: true,
		},
	}
	finalize_block_resp, _ := client.Do(req)
	defer finalize_block_resp.Body.Close()

	body, err := ioutil.ReadAll(finalize_block_resp.Body)
	if err != nil {
		return nil
	}

	var res JSONRPCResponse
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil
	}

	return nil
}

// PollBlockChanges opens a polling loop to fetch the L1 block reference with the given label,
// on provided interval and with request timeout. Results are returned with provided callback fn,
// which may block to pause/back-pressure polling.
func (s *SbbService) PollBlockChanges(interval time.Duration, timeout time.Duration) ethereum.Subscription {
	return event.NewSubscription(func(quit <-chan struct{}) error {
		if interval <= 0 {
			log.Warn("polling of block is disabled", "interval", interval)
			<-quit
			return nil
		}
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

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				reqCtx, reqCancel := context.WithTimeout(eventsCtx, timeout)
				//ref, err := src.L1BlockRefByLabel(reqCtx, label)
				s.sbbClient.c
				//SBB에 요청
				reqCancel()
				if err != nil {
					//log.Warn("failed to poll L1 block", "label", label, "err", err)
				} else {
					//fn(eventsCtx, ref)
					// txpool처리
				}
			case <-eventsCtx.Done():
				return nil
			}
		}
	})
}
