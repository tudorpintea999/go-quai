// Copyright 2015 The go-ethereum Authors
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

package core

import (
	"fmt"
	"math/big"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/consensus"
	"github.com/dominant-strategies/go-quai/consensus/misc"
	"github.com/dominant-strategies/go-quai/core/state"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/core/vm"
	"github.com/dominant-strategies/go-quai/ethdb"
	"github.com/dominant-strategies/go-quai/params"
)

// BlockGen creates blocks for testing.
// See GenerateChain for a detailed explanation.
type BlockGen struct {
	i       int
	parent  *types.Block
	chain   []*types.Block
	header  *types.Header
	statedb *state.StateDB

	gasPool     *GasPool
	txs         []*types.Transaction
	receipts    []*types.Receipt
	uncles      []*types.Header
	etxs        []*types.Transaction
	subManifest types.BlockManifest

	config *params.ChainConfig
	engine consensus.Engine
}

// SetCoinbase sets the coinbase of the generated block.
// It can be called at most once.
func (b *BlockGen) SetCoinbase(addr common.Address) {
	if b.gasPool != nil {
		if len(b.txs) > 0 {
			panic("coinbase must be set before adding transactions")
		}
		panic("coinbase can only be set once")
	}
	b.header.SetCoinbase(addr)
	b.gasPool = new(GasPool).AddGas(b.header.GasLimit())
}

// SetExtra sets the extra data field of the generated block.
func (b *BlockGen) SetExtra(data []byte) {
	b.header.SetExtra(data)
}

// SetNonce sets the nonce field of the generated block.
func (b *BlockGen) SetNonce(nonce types.BlockNonce) {
	b.header.SetNonce(nonce)
}

// SetDifficulty sets the difficulty field of the generated block. This method is
// useful for Clique tests where the difficulty does not depend on time. For the
// progpow tests, please use OffsetTime, which implicitly recalculates the diff.
func (b *BlockGen) SetDifficulty(diff *big.Int) {
	b.header.SetDifficulty(diff)
}

// AddTx adds a transaction to the generated block. If no coinbase has
// been set, the block's coinbase is set to the zero address.
//
// AddTx panics if the transaction cannot be executed. In addition to
// the protocol-imposed limitations (gas limit, etc.), there are some
// further limitations on the content of transactions that can be
// added. Notably, contract code relying on the BLOCKHASH instruction
// will panic during execution.
func (b *BlockGen) AddTx(tx *types.Transaction, etxRLimit, etxPLimit *int) {
	b.AddTxWithChain(nil, tx, etxRLimit, etxPLimit)
}

// AddTxWithChain adds a transaction to the generated block. If no coinbase has
// been set, the block's coinbase is set to the zero address.
//
// AddTxWithChain panics if the transaction cannot be executed. In addition to
// the protocol-imposed limitations (gas limit, etc.), there are some
// further limitations on the content of transactions that can be
// added. If contract code relies on the BLOCKHASH instruction,
// the block in chain will be returned.
func (b *BlockGen) AddTxWithChain(hc *HeaderChain, tx *types.Transaction, etxRLimit, etxPLimit *int) {
	if b.gasPool == nil {
		b.SetCoinbase(common.ZeroAddress(hc.config.Location))
	}
	b.statedb.Prepare(tx.Hash(), len(b.txs))
	coinbase := b.header.Coinbase()
	gasUsed := b.header.GasUsed()
	receipt, err := ApplyTransaction(b.config, hc, &coinbase, b.gasPool, b.statedb, b.header, tx, &gasUsed, vm.Config{}, etxRLimit, etxPLimit, hc.logger)
	if err != nil {
		panic(err)
	}
	b.txs = append(b.txs, tx)
	b.receipts = append(b.receipts, receipt)
}

// GetBalance returns the balance of the given address at the generated block.
func (b *BlockGen) GetBalance(addr common.Address) *big.Int {
	internal, err := addr.InternalAddress()
	if err != nil {
		panic(err.Error())
	}
	return b.statedb.GetBalance(internal)
}

// AddUncheckedTx forcefully adds a transaction to the block without any
// validation.
//
// AddUncheckedTx will cause consensus failures when used during real
// chain processing. This is best used in conjunction with raw block insertion.
func (b *BlockGen) AddUncheckedTx(tx *types.Transaction) {
	b.txs = append(b.txs, tx)
}

// Number returns the block number of the block being generated.
func (b *BlockGen) Number() *big.Int {
	return new(big.Int).Set(b.header.Number(b.config.Location.Context()))
}

// BaseFee returns the base fee of the block being generated.
func (b *BlockGen) BaseFee() *big.Int {
	return new(big.Int).Set(b.header.BaseFee())
}

// AddUncheckedReceipt forcefully adds a receipts to the block without a
// backing transaction.
//
// AddUncheckedReceipt will cause consensus failures when used during real
// chain processing. This is best used in conjunction with raw block insertion.
func (b *BlockGen) AddUncheckedReceipt(receipt *types.Receipt) {
	b.receipts = append(b.receipts, receipt)
}

// TxNonce returns the next valid transaction nonce for the
// account at addr. It panics if the account does not exist.
func (b *BlockGen) TxNonce(addr common.Address) uint64 {
	internal, err := addr.InternalAddress()
	if err != nil {
		panic(err.Error())
	}
	if !b.statedb.Exist(internal) {
		panic("account does not exist")
	}
	return b.statedb.GetNonce(internal)
}

// AddUncle adds an uncle header to the generated block.
func (b *BlockGen) AddUncle(h *types.Header) {
	b.uncles = append(b.uncles, h)
}

// PrevBlock returns a previously generated block by number. It panics if
// num is greater or equal to the number of the block being generated.
// For index -1, PrevBlock returns the parent block given to GenerateChain.
func (b *BlockGen) PrevBlock(index int) *types.Block {
	if index >= b.i {
		panic(fmt.Errorf("block index %d out of range (%d,%d)", index, -1, b.i))
	}
	if index == -1 {
		return b.parent
	}
	return b.chain[index]
}

// OffsetTime modifies the time instance of a block, implicitly changing its
// associated difficulty. It's useful to test scenarios where forking is not
// tied to chain length directly.
func (b *BlockGen) OffsetTime(seconds int64) {
	b.header.SetTime(b.header.Time() + uint64(seconds))
	if b.header.Time() <= b.parent.Header().Time() {
		panic("block time out of range")
	}
	chainreader := &fakeChainReader{config: b.config}
	b.header.SetDifficulty(b.engine.CalcDifficulty(chainreader, b.parent.Header()))
}

// GenerateChain creates a chain of n blocks. The first block's
// parent will be the provided parent. db is used to store
// intermediate states and should contain the parent's state trie.
//
// The generator function is called with a new block generator for
// every block. Any transactions and uncles added to the generator
// become part of the block. If gen is nil, the blocks will be empty
// and their coinbase will be the zero address.
//
// Blocks created by GenerateChain do not contain valid proof of work
// values. Inserting them into BlockChain requires use of FakePow or
// a similar non-validating proof of work implementation.
func GenerateChain(config *params.ChainConfig, parent *types.Block, engine consensus.Engine, db ethdb.Database, n int, gen func(int, *BlockGen)) ([]*types.Block, []types.Receipts) {
	if config == nil {
		config = params.TestChainConfig
	}
	blocks, receipts := make(types.Blocks, n), make([]types.Receipts, n)
	chainreader := &fakeChainReader{config: config}
	genblock := func(i int, parent *types.Block, statedb *state.StateDB) (*types.Block, types.Receipts) {
		b := &BlockGen{i: i, chain: blocks, parent: parent, statedb: statedb, config: config, engine: engine}
		b.header = makeHeader(chainreader, parent, statedb, b.engine)

		// Execute any user modifications to the block
		if gen != nil {
			gen(i, b)
		}
		if b.engine != nil {
			// Finalize and seal the block
			block, _ := b.engine.FinalizeAndAssemble(chainreader, b.header, statedb, b.txs, b.uncles, b.etxs, b.subManifest, b.receipts)

			// Write state changes to db
			root, err := statedb.Commit(true)
			if err != nil {
				panic(fmt.Sprintf("state write error: %v", err))
			}
			utxoRoot, err := statedb.CommitUTXOs()
			if err != nil {
				panic(fmt.Sprintf("state write error: %v", err))
			}
			if err := statedb.Database().TrieDB().Commit(root, false, nil); err != nil {
				panic(fmt.Sprintf("trie write error: %v", err))
			}
			if err := statedb.UTXODatabase().TrieDB().Commit(utxoRoot, false, nil); err != nil {
				panic(fmt.Sprintf("trie write error: %v", err))
			}
			return block, b.receipts
		}
		return nil, nil
	}
	for i := 0; i < n; i++ {
		statedb, err := state.New(parent.Root(), parent.UTXORoot(), state.NewDatabase(db), state.NewDatabase(db), nil, config.Location)
		if err != nil {
			panic(err)
		}
		block, receipt := genblock(i, parent, statedb)
		blocks[i] = block
		receipts[i] = receipt
		parent = block
	}
	return blocks, receipts
}

func makeHeader(chain consensus.ChainReader, parent *types.Block, state *state.StateDB, engine consensus.Engine) *types.Header {
	var time uint64
	if parent.Time() == 0 {
		time = 10
	} else {
		time = parent.Time() + 10 // block time is fixed at 10 seconds
	}
	nodeCtx := chain.Config().Location.Context()

	// Temporary header values just to calc difficulty
	diffheader := types.EmptyHeader()
	diffheader.SetDifficulty(parent.Difficulty(nodeCtx))
	diffheader.SetNumber(parent.Number(nodeCtx), nodeCtx)
	diffheader.SetTime(time - 10)
	diffheader.SetUncleHash(parent.UncleHash())

	// Make new header
	header := types.EmptyHeader()
	header.SetUTXORoot(state.UTXORoot())
	header.SetRoot(state.IntermediateRoot(true))
	header.SetParentHash(parent.Hash(), nodeCtx)
	header.SetCoinbase(parent.Coinbase())
	header.SetDifficulty(engine.CalcDifficulty(chain, diffheader))
	header.SetGasLimit(parent.GasLimit())
	header.SetNumber(new(big.Int).Add(parent.Number(nodeCtx), common.Big1), nodeCtx)
	header.SetTime(time)
	header.SetBaseFee(misc.CalcBaseFee(chain.Config(), parent.Header()))
	return header
}

// makeHeaderChain creates a deterministic chain of headers rooted at parent.
func makeHeaderChain(parent *types.Header, n int, engine consensus.Engine, db ethdb.Database, seed int) []*types.Header {
	blocks := makeBlockChain(types.NewBlockWithHeader(parent), n, engine, db, seed)
	headers := make([]*types.Header, len(blocks))
	for i, block := range blocks {
		headers[i] = block.Header()
	}
	return headers
}

// makeBlockChain creates a deterministic chain of blocks rooted at parent.
func makeBlockChain(parent *types.Block, n int, engine consensus.Engine, db ethdb.Database, seed int) []*types.Block {
	blocks, _ := GenerateChain(params.TestChainConfig, parent, engine, db, n, func(i int, b *BlockGen) {
		b.SetCoinbase(common.BytesToAddress(common.InternalAddress{0: byte(seed), 19: byte(i)}.Bytes(), params.TestChainConfig.Location))
	})
	return blocks
}

type fakeChainReader struct {
	config *params.ChainConfig
}

// Config returns the chain configuration.
func (cr *fakeChainReader) Config() *params.ChainConfig {
	return cr.config
}

func (cr *fakeChainReader) CurrentHeader() *types.Header                            { return nil }
func (cr *fakeChainReader) GetHeaderByNumber(number uint64) *types.Header           { return nil }
func (cr *fakeChainReader) GetHeaderByHash(hash common.Hash) *types.Header          { return nil }
func (cr *fakeChainReader) GetHeader(hash common.Hash, number uint64) *types.Header { return nil }
func (cr *fakeChainReader) GetBlock(hash common.Hash, number uint64) *types.Block   { return nil }
func (cr *fakeChainReader) GetTerminiByHash(hash common.Hash) *types.Termini        { return nil }
func (cr *fakeChainReader) ProcessingState() bool                                   { return false }
