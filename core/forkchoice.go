// Copyright 2021 The go-ethereum Authors
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
	crand "crypto/rand"
	"errors"
	"math/big"
	mrand "math/rand"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

// ChainReader defines a small collection of methods needed to access the local
// blockchain during header verification. It's implemented by both blockchain
// and lightchain.
type ChainReader interface {
	// Config retrieves the header chain's chain configuration.
	Config() *params.ChainConfig

	// GetTd returns the total difficulty of a local block.
	GetTd(common.Hash, uint64) *big.Int
}

type ForkChoiceEIP3436 struct {
	chain ChainReader
	rand  *mrand.Rand

	// legacy remenant from old forkchoice
	preserve func(header *types.Header) bool
}

func NewForkChoiceEIP3436(chain ChainReader, preserve func(header *types.Header) bool) *ForkChoiceEIP3436 {
	// Seed a fast but crypto originating random generator
	seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		log.Crit("Failed to initialize random seed", "err", err)
	}

	return &ForkChoiceEIP3436{
		chain:    chain,
		rand:     mrand.New(mrand.NewSource(seed.Int64())),
		preserve: preserve,
	}
}

// ReorgNeeded returns whether the reorg should be applied
// based on the given external header and local canonical chain.
func (f *ForkChoiceEIP3436) ReorgNeeded(current *types.Header, extern *types.Header) (bool, error) {
	var (
		localTD  = f.chain.GetTd(current.Hash(), current.Number.Uint64())
		externTD = f.chain.GetTd(extern.Hash(), extern.Number.Uint64())
	)
	if localTD == nil || externTD == nil {
		return false, errors.New("missing td")
	}

	// Rule 1: Choose the block with the most total difficulty.
	if localTD.Cmp(externTD) > 0 {
		return false, nil
	} else if localTD.Cmp(externTD) < 0 {
		return true, nil
	}

	// Rule 2: Choose the block with the lowest block number.
	if current.Number.Cmp(extern.Number) < 0 {
		return false, nil
	} else if current.Number.Cmp(extern.Number) > 0 {
		return true, nil
	}

	// Rule 3: Choose the block whose validator had the least recent in-turn block assignment.
	// (header_number - validator_index) % validator_count
	// TODO(@ckartik): Figure out how to get the validator count and most recent in-turn block assignment
	validatorCount := f.chain.Config().Clique.Validators
	currentValidatorIndex := int(current.Coinbase.Big().Int64()) % validatorCount
	externValidatorIndex := int(extern.Coinbase.Big().Int64()) % validatorCount

	currentInTurn := (current.Number.Uint64() - uint64(currentValidatorIndex)) % uint64(validatorCount)
	externInTurn := (extern.Number.Uint64() - uint64(externValidatorIndex)) % uint64(validatorCount)

	if currentInTurn > externInTurn {
		return false, nil
	} else if currentInTurn < externInTurn {
		return true, nil
	}

	// Rule 4: Choose the block with the lowest hash.
	currentHash := new(big.Int).SetBytes(current.Hash().Bytes())
	externHash := new(big.Int).SetBytes(extern.Hash().Bytes())

	if currentHash.Cmp(externHash) < 0 {
		return false, nil
	} else if currentHash.Cmp(externHash) > 0 {
		return true, nil
	}

	log.Warn("Potential deadlock state detected: all fork choice rules resulted in a tie",
		"currentTD", localTD, "externTD", externTD,
		"currentNumber", current.Number, "externNumber", extern.Number,
		"currentInTurn", currentInTurn, "externInTurn", externInTurn,
		"currentHash", currentHash, "externHash", externHash)

	return false, nil
}

// ForkChoice is the fork chooser based on the highest total difficulty of the
// chain(the fork choice used in the eth1) and the external fork choice (the fork
// choice used in the eth2). This main goal of this ForkChoice is not only for
// offering fork choice during the eth1/2 merge phase, but also keep the compatibility
// for all other proof-of-work networks.
type ForkChoice struct {
	chain ChainReader
	rand  *mrand.Rand

	// preserve is a helper function used in td fork choice.
	// Miners will prefer to choose the local mined block if the
	// local td is equal to the extern one. It can be nil for light
	// client
	preserve func(header *types.Header) bool
}

func NewForkChoice(chainReader ChainReader, preserve func(header *types.Header) bool) *ForkChoice {
	// Seed a fast but crypto originating random generator
	seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		log.Crit("Failed to initialize random seed", "err", err)
	}
	return &ForkChoice{
		chain:    chainReader,
		rand:     mrand.New(mrand.NewSource(seed.Int64())),
		preserve: preserve,
	}
}

// ReorgNeeded returns whether the reorg should be applied
// based on the given external header and local canonical chain.
// In the td mode, the new head is chosen if the corresponding
// total difficulty is higher. In the extern mode, the trusted
// header is always selected as the head.
func (f *ForkChoice) ReorgNeeded(current *types.Header, extern *types.Header) (bool, error) {
	var (
		localTD  = f.chain.GetTd(current.Hash(), current.Number.Uint64())
		externTd = f.chain.GetTd(extern.Hash(), extern.Number.Uint64())
	)
	if localTD == nil || externTd == nil {
		return false, errors.New("missing td")
	}
	// Accept the new header as the chain head if the transition
	// is already triggered. We assume all the headers after the
	// transition come from the trusted consensus layer.
	if ttd := f.chain.Config().TerminalTotalDifficulty; ttd != nil && ttd.Cmp(externTd) <= 0 {
		return true, nil
	}

	// If the total difficulty is higher than our known, add it to the canonical chain
	if diff := externTd.Cmp(localTD); diff > 0 {
		return true, nil
	} else if diff < 0 {
		return false, nil
	}
	// Local and external difficulty is identical.
	// Second clause in the if statement reduces the vulnerability to selfish mining.
	// Please refer to http://www.cs.cornell.edu/~ie53/publications/btcProcFC.pdf
	reorg := false
	externNum, localNum := extern.Number.Uint64(), current.Number.Uint64()
	if externNum < localNum {
		reorg = true
	} else if externNum == localNum {
		var currentPreserve, externPreserve bool
		if f.preserve != nil {
			currentPreserve, externPreserve = f.preserve(current), f.preserve(extern)
		}
		reorg = !currentPreserve && (externPreserve || f.rand.Float64() < 0.5)
	}
	return reorg, nil
}
