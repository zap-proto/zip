// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// What a node tells anyone who asks — the conformance corpus, in Go.
//
// This service exists three times: here, in rust/examples/info, and in
// cpp/examples/info. The three are written in their own languages, in their own
// idioms, and each one's framework projects it without help from the other two.
// The projections they produce are then compared byte for byte, which is the
// only proof that zip is ONE framework with three implementations rather than
// one with two clients.
//
// The shapes are chosen to be the ones that break: a quoted decimal (a value
// whose JSON form is not what it is made of), a nested named struct, a list of
// structs, a map, a path-free GET with a query parameter, and an op that takes
// nothing at all.
package info

import "strconv"

// Uint64 is a 64-bit count carried as a quoted decimal, because JSON numbers
// lose precision above 2^53 and a stake does not.
//
// It is the case that makes the manifest carry two answers for one type: to a
// JSON reader this is a string, and to a fixed layout it is eight bytes. A
// description that could only say one of those would make one projection lie.
type Uint64 uint64

func (u Uint64) MarshalJSON() ([]byte, error) {
	return strconv.AppendQuote(nil, strconv.FormatUint(uint64(u), 10)), nil
}

func (u *Uint64) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return err
	}
	*u = Uint64(n)
	return nil
}

// MarshalText and UnmarshalText are the same value as ONE WORD, which is what a
// URL can carry. A quoted decimal is already text; saying so is what lets it be
// a query parameter as well as a body field.
func (u Uint64) MarshalText() ([]byte, error) {
	return strconv.AppendUint(nil, uint64(u), 10), nil
}

func (u *Uint64) UnmarshalText(b []byte) error {
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return err
	}
	*u = Uint64(n)
	return nil
}

// JSONSchema states this type's wire form, next to the code that writes it.
func (Uint64) JSONSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": "^[0-9]+$"}
}

// NodeVersion is what this node is running.
type NodeVersion struct {
	// Version is the node's own release.
	Version string `json:"version"`
	// DatabaseVersion is the database format this node reads.
	DatabaseVersion string `json:"databaseVersion"`
	// RPCProtocolVersion is the VM protocol this node speaks.
	RPCProtocolVersion uint32 `json:"rpcProtocolVersion"`
	// GitCommit is the commit this node was built from.
	GitCommit string `json:"gitCommit"`
}

// NodeID is this node's id, with the proof it holds the staking key.
type NodeID struct {
	// NodeID is the id peers know this node by.
	NodeID string `json:"nodeID"`
	// NodePOP is the proof this node holds the key its id is derived from.
	NodePOP ProofOfPossession `json:"nodePOP"`
}

// ProofOfPossession is a staking key and a signature over it.
type ProofOfPossession struct {
	// PublicKey is the BLS public key, hex encoded.
	PublicKey string `json:"publicKey"`
	// ProofOfPossession is the signature over that key, hex encoded.
	ProofOfPossession string `json:"proofOfPossession"`
}

// NodeIP is where this node tells its peers to reach it.
type NodeIP struct {
	// IP is the address and port peers dial.
	IP string `json:"ip"`
}

// NetworkID is the number of the network a node is on.
type NetworkID struct {
	// NetworkID is the network's number.
	NetworkID uint32 `json:"networkID"`
}

// NetworkName is the name of the network a node is on.
type NetworkName struct {
	// NetworkName is the network's name.
	NetworkName string `json:"networkName"`
}

// ChainAlias names the chain a caller is asking about.
type ChainAlias struct {
	// Alias is the name the chain answers to, such as "X".
	Alias string `json:"alias" validate:"required"`
}

// ChainID is the id an alias names.
type ChainID struct {
	// BlockchainID is the chain's id.
	BlockchainID string `json:"blockchainID"`
}

// BootstrappedArgs names the chain whose bootstrap state is being asked about.
type BootstrappedArgs struct {
	// Chain is the alias or id of the chain to ask about.
	Chain string `json:"chain" validate:"required"`
}

// Bootstrapped is whether a chain has finished bootstrapping.
type Bootstrapped struct {
	// IsBootstrapped is true once the chain has caught up.
	IsBootstrapped bool `json:"isBootstrapped"`
}

// Chain is one chain this node runs.
type Chain struct {
	// ID is the chain's id.
	ID string `json:"id"`
	// Name is the chain's name.
	Name string `json:"name"`
	// VMID is the id of the virtual machine the chain runs.
	VMID string `json:"vmID"`
	// Bootstrapped is whether this node has caught the chain up.
	Bootstrapped bool `json:"bootstrapped"`
}

// Chains are the chains this node runs.
type Chains struct {
	// Chains are the chains this node is running.
	Chains []Chain `json:"chains"`
}

// PeersArgs narrows the peer list to the nodes named.
type PeersArgs struct {
	// NodeIDs are the peers to answer for; none asks for all of them.
	NodeIDs []string `json:"nodeIDs"`
}

// Peer is one connection this node holds.
type Peer struct {
	// IP is the address this node reaches the peer at.
	IP string `json:"ip"`
	// PublicIP is the address the peer tells others to use.
	PublicIP string `json:"publicIP"`
	// NodeID is the peer's id.
	NodeID string `json:"nodeID"`
	// Version is the release the peer is running.
	Version string `json:"version"`
	// ObservedUptime is the share of time this node has seen the peer up.
	ObservedUptime uint32 `json:"observedUptime"`
	// TrackedChains are the chains the peer is following.
	TrackedChains []string `json:"trackedChains"`
}

// Peers are the nodes this one is connected to.
type Peers struct {
	// NumPeers is how many connections this node holds.
	NumPeers Uint64 `json:"numPeers"`
	// Peers are the connections themselves.
	Peers []Peer `json:"peers"`
}

// LP is where the stake stands on one proposal.
type LP struct {
	// SupportWeight is the stake in favour.
	SupportWeight Uint64 `json:"supportWeight"`
	// ObjectWeight is the stake against.
	ObjectWeight Uint64 `json:"objectWeight"`
	// AbstainWeight is the stake that has not said.
	AbstainWeight Uint64 `json:"abstainWeight"`
}

// LPs is where the network's stake stands on every current proposal.
type LPs struct {
	// LPs are the proposals, by number.
	LPs map[string]LP `json:"lps"`
}

// VMs are the virtual machines installed on this node.
type VMs struct {
	// VMs are the installed machines, by id, each with the names it answers to.
	VMs map[string][]string `json:"vms"`
	// Fxs are the feature extensions the UTXO chains understand, by id.
	Fxs map[string]string `json:"fxs"`
}

// Upgrades is the upgrade schedule this node runs.
type Upgrades struct {
	// StopVertexID is the vertex the linearisation stopped at.
	StopVertexID string `json:"stopVertexID"`
	// EpochDuration is how long one epoch lasts, in nanoseconds.
	EpochDuration Uint64 `json:"epochDuration"`
}

// Uptime is how much of the network reports having seen this node up.
type Uptime struct {
	// RewardingStakePercentage is the share of stake that would reward this node.
	RewardingStakePercentage string `json:"rewardingStakePercentage"`
	// WeightedAveragePercentage is the stake-weighted average uptime.
	WeightedAveragePercentage string `json:"weightedAveragePercentage"`
}

// Fees are the transaction fees this node charges, in nLUX.
type Fees struct {
	// TxFee is the fee for an ordinary transaction.
	TxFee Uint64 `json:"txFee"`
	// CreateAssetTxFee is the fee to create an asset.
	CreateAssetTxFee Uint64 `json:"createAssetTxFee"`
	// CreateChainTxFee is the fee to create a chain.
	CreateChainTxFee Uint64 `json:"createChainTxFee"`
}
