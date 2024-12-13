// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"container/heap"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

type txWithSeq struct {
	tx   *txpool.LazyTransaction
	from common.Address
	fees *uint256.Int
	seq  int
}

func newTxWithSeq(tx *txpool.LazyTransaction, from common.Address, baseFee *uint256.Int) (*txWithSeq, error) {
	tip := new(uint256.Int).Set(tx.GasTipCap)
	if baseFee != nil {
		if tx.GasFeeCap.Cmp(baseFee) < 0 {
			return nil, types.ErrGasFeeCapTooLow
		}
		tip = new(uint256.Int).Sub(tx.GasFeeCap, baseFee)
		if tip.Gt(tx.GasTipCap) {
			tip = tx.GasTipCap
		}
	}
	return &txWithSeq{
		tx:   tx,
		from: from,
		fees: tip,
		seq:  tx.Tx.Seq(),
	}, nil
}

type txBySeqAndTime []*txWithSeq

func compare(a int, b int) int {
	if a > b {
		return 1
	} else if a < b {
		return -1
	}
	return 0
}

func (s txBySeqAndTime) Len() int { return len(s) }
func (s txBySeqAndTime) Less(i, j int) bool {
	cmp := compare(s[i].seq, s[j].seq)
	if cmp == 0 {
		return s[i].tx.Time.Before(s[j].tx.Time)
	}
	return cmp > 0
}
func (s txBySeqAndTime) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

func (s *txBySeqAndTime) Push(x interface{}) {
	*s = append(*s, x.(*txWithSeq))
}

func (s *txBySeqAndTime) Pop() interface{} {
	old := *s
	n := len(old)
	x := old[n-1]
	old[n-1] = nil
	*s = old[0 : n-1]
	return x
}

// transactionsByPriceAndNonce represents a set of transactions that can return
// transactions in a profit-maximizing sorted order, while supporting removing
// entire batches of transactions for non-executable accounts.
type transactionsByPriceAndNonce struct {
	txs     map[common.Address][]*txpool.LazyTransaction // Per account nonce-sorted list of transactions
	heads   txBySeqAndTime                               // Next transaction for each unique account (price heap)
	signer  types.Signer                                 // Signer for the set of transactions
	baseFee *uint256.Int                                 // Current base fee
}

type transactionsBySeqAndNonce struct {
	txs     map[common.Address][]*txpool.LazyTransaction // Per account nonce-sorted list of transactions
	heads   txBySeqAndTime                               // Next transaction for each unique account (price heap)
	signer  types.Signer                                 // Signer for the set of transactions
	baseFee *uint256.Int                                 // Current base fee
}

func newTransactionsBySeqAndNonce(signer types.Signer, txs map[common.Address][]*txpool.LazyTransaction, baseFee *big.Int) *transactionsBySeqAndNonce {
	// Convert the basefee from header format to uint256 format
	var baseFeeUint *uint256.Int
	if baseFee != nil {
		baseFeeUint = uint256.MustFromBig(baseFee)
	}

	heads := make(txBySeqAndTime, 0, len(txs))
	for from, accTxs := range txs {
		wrapped, err := newTxWithSeq(accTxs[0], from, baseFeeUint)
		if err != nil {
			delete(txs, from)
			continue
		}
		heads = append(heads, wrapped)
		txs[from] = accTxs[1:]
	}
	heap.Init(&heads)

	// Assemble and return the transaction set
	return &transactionsBySeqAndNonce{
		txs:     txs,
		heads:   heads,
		signer:  signer,
		baseFee: baseFeeUint,
	}
}

// Pop removes the best transaction, *not* replacing it with the next one from
// the same account. This should be used when a transaction cannot be executed
// and hence all subsequent ones should be discarded from the same account.
func (t *transactionsByPriceAndNonce) Pop() {
	heap.Pop(&t.heads)
}

// Empty returns if the price heap is empty. It can be used to check it simpler
// than calling peek and checking for nil return.
func (t *transactionsByPriceAndNonce) Empty() bool {
	return len(t.heads) == 0
}

// Clear removes the entire content of the heap.
func (t *transactionsByPriceAndNonce) Clear() {
	t.heads, t.txs = nil, nil
}

// Peek returns the next transaction by price.
func (t *transactionsBySeqAndNonce) Peek() (*txpool.LazyTransaction, *uint256.Int) {
	if len(t.heads) == 0 {
		return nil, nil
	}
	return t.heads[0].tx, t.heads[0].fees
}

// Shift replaces the current best head with the next one from the same account.
func (t *transactionsBySeqAndNonce) Shift() {
	acc := t.heads[0].from
	if txs, ok := t.txs[acc]; ok && len(txs) > 0 {
		if wrapped, err := newTxWithSeq(txs[0], acc, t.baseFee); err == nil {
			t.heads[0], t.txs[acc] = wrapped, txs[1:]
			heap.Fix(&t.heads, 0)
			return
		}
	}
	heap.Pop(&t.heads)
}

// Pop removes the best transaction, *not* replacing it with the next one from
// the same account. This should be used when a transaction cannot be executed
// and hence all subsequent ones should be discarded from the same account.
func (t *transactionsBySeqAndNonce) Pop() {
	heap.Pop(&t.heads)
}

// Empty returns if the price heap is empty. It can be used to check it simpler
// than calling peek and checking for nil return.
func (t *transactionsBySeqAndNonce) Empty() bool {
	return len(t.heads) == 0
}

// Clear removes the entire content of the heap.
func (t *transactionsBySeqAndNonce) Clear() {
	t.heads, t.txs = nil, nil
}
