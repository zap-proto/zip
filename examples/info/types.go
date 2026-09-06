// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package info

// What a node says about itself, as the wire carries it.
//
// These are a transcription of the shapes github.com/luxfi/api/info publishes,
// spelled in plain types: the real ones carry numbers as quoted decimals and
// several lists as objects, which is a property of that API's history and not of
// the framework. What is faithful here is what this corpus is for — the ops,
// their addresses, their prose and the SHAPE of what they answer — because the
// same service is written again in C++ beside this one, and the two must project
// to the same bytes.

// VMVersion is one VM and the version of it a node runs.
type VMVersion struct {
	// VM is the VM's alias.
	VM string `json:"vm"`
	// Version is the release of it this node runs.
	Version string `json:"version"`
}

// ConsensusInfo summarises the consensus configuration of a running node, so a
// caller does not have to scrape the boot logs to learn what it is running.
type ConsensusInfo struct {
	// Mode is "triple" (BLS + Corona + ML-DSA), "dual" (BLS + Corona), or
	// "classical" (BLS only).
	Mode string `json:"mode"`
	// BLS is always true for a production Quasar node.
	BLS bool `json:"bls"`
	// Corona is true when the post-quantum lattice threshold path is wired.
	Corona bool `json:"corona"`
	// MLDSA is true when ML-DSA-65 signature verification is wired.
	MLDSA bool `json:"mlDSA"`
	// PlatformVM is true when the production PlatformVM wiring is in use.
	PlatformVM bool `json:"platformVM"`
}

// GetNodeVersionReply is what this node is running.
type GetNodeVersionReply struct {
	Version            string        `json:"version"`
	DatabaseVersion    string        `json:"databaseVersion"`
	RPCProtocolVersion uint32        `json:"rpcProtocolVersion"`
	GitCommit          string        `json:"gitCommit"`
	VMVersions         []VMVersion   `json:"vmVersions"`
	Consensus          ConsensusInfo `json:"consensus"`
}

// ProofOfPossession is a BLS proof of possession, hex-encoded.
type ProofOfPossession struct {
	PublicKey         string `json:"publicKey"`
	ProofOfPossession string `json:"proofOfPossession"`
}

// GetNodeIDReply is this node's id and the proof it holds the staking key.
type GetNodeIDReply struct {
	NodeID  string            `json:"nodeID"`
	NodePOP ProofOfPossession `json:"nodePOP"`
}

// GetNodeIPReply is where this node tells its peers to reach it.
type GetNodeIPReply struct {
	IP string `json:"ip"`
}

// GetNetworkIDReply is the number of the network this node is on.
type GetNetworkIDReply struct {
	NetworkID uint32 `json:"networkID"`
}

// GetNetworkNameReply is the name of the network this node is on.
type GetNetworkNameReply struct {
	NetworkName string `json:"networkName"`
}

// GetBlockchainIDArgs names a chain by an alias.
type GetBlockchainIDArgs struct {
	// Alias is the name the chain answers to, such as "X".
	Alias string `json:"alias" validate:"required"`
}

// GetBlockchainIDReply is the id the alias names.
type GetBlockchainIDReply struct {
	BlockchainID string `json:"blockchainID"`
}

// IsBootstrappedArgs names the chain to ask about.
type IsBootstrappedArgs struct {
	// Chain is the alias or id of the chain to ask about.
	Chain string `json:"chain" validate:"required"`
}

// IsBootstrappedResponse is whether that chain has finished.
type IsBootstrappedResponse struct {
	IsBootstrapped bool `json:"isBootstrapped"`
}

// ChainInfo is one chain this node runs.
type ChainInfo struct {
	// ID is the chain's id.
	ID string `json:"id"`
	// Name is what it is called.
	Name string `json:"name"`
	// VMID is the VM it runs.
	VMID string `json:"vmID"`
	// Bootstrapped is whether it has finished bootstrapping here.
	Bootstrapped bool `json:"bootstrapped"`
}

// GetChainsReply is the chains this node is running.
type GetChainsReply struct {
	// Chains are the chains this node is running.
	Chains []ChainInfo `json:"chains"`
}

// PeersArgs narrows the answer to the peers named.
type PeersArgs struct {
	// NodeIDs are the peers to answer for; none asks for all of them.
	NodeIDs []string `json:"nodeIDs"`
}

// PeerInfo is one live connection as the wire carries it.
type PeerInfo struct {
	// IP is the address this node reaches the peer at.
	IP string `json:"ip"`
	// PublicIP is the address the peer says it is reachable at.
	PublicIP string `json:"publicIP"`
	// ID is the peer's node id.
	ID string `json:"nodeID"`
	// Version is the node software the peer is running.
	Version string `json:"version"`
	// LastSent is when this node last sent the peer a message.
	LastSent string `json:"lastSent"`
	// LastReceived is when this node last heard from the peer.
	LastReceived string `json:"lastReceived"`
	// ObservedUptime is the percentage of time this node has seen the peer up.
	ObservedUptime uint32 `json:"observedUptime"`
	// TrackedChains are the chains the peer serves, ordered by id.
	TrackedChains []string `json:"trackedChains"`
	// SupportedLPs are the LPs the peer votes for, ascending.
	SupportedLPs []uint32 `json:"supportedLPs"`
	// ObjectedLPs are the LPs the peer votes against, ascending.
	ObjectedLPs []uint32 `json:"objectedLPs"`
}

// Peer is a connection, with what this node has benched it for.
type Peer struct {
	PeerInfo
	// Benched are the chains this node has stopped talking to it about.
	Benched []string `json:"benched"`
}

// PeersReply is who this node is connected to.
type PeersReply struct {
	// NumPeers is how many there are.
	NumPeers uint64 `json:"numPeers"`
	// Peers are the connections themselves.
	Peers []Peer `json:"peers"`
}

// LP is the stake for, against and abstaining on one proposal.
type LP struct {
	// SupportWeight is the stake voting for it.
	SupportWeight uint64 `json:"supportWeight"`
	// Supporters are the peers voting for it, ordered by node id.
	Supporters []string `json:"supporters"`
	// ObjectWeight is the stake voting against it.
	ObjectWeight uint64 `json:"objectWeight"`
	// Objectors are the peers voting against it, ordered by node id.
	Objectors []string `json:"objectors"`
	// AbstainWeight is the stake that has said nothing.
	AbstainWeight uint64 `json:"abstainWeight"`
}

// LPStatus is one proposal and where the stake stands on it.
type LPStatus struct {
	// Number is the LP's number, the key it appears under on the JSON wire.
	Number uint32 `json:"number"`
	// LP is the stake for, against and abstaining.
	LP LP `json:"lp"`
}

// LPsReply is where the stake stands on every current proposal.
type LPsReply struct {
	// LPs are the proposals, ascending by number.
	LPs []LPStatus `json:"lps"`
}

// VMAlias is one VM installed on this node.
type VMAlias struct {
	// VM is the VM's id.
	VM string `json:"vm"`
	// Aliases are the names it also answers to.
	Aliases []string `json:"aliases"`
}

// FxName is one feature extension the UTXO chains understand.
type FxName struct {
	// Fx is the extension's id.
	Fx string `json:"fx"`
	// Name is what it is called.
	Name string `json:"name"`
}

// GetVMsReply is what is installed on this node.
type GetVMsReply struct {
	// VMs are the virtual machines installed here.
	VMs []VMAlias `json:"vms"`
	// Fxs are the feature extensions the UTXO chains understand.
	Fxs []FxName `json:"fxs"`
}

// UpgradeConfig is the upgrade schedule this node runs.
type UpgradeConfig struct {
	// XChainStopVertexID is where the X-Chain stopped being a DAG.
	XChainStopVertexID string `json:"xChainStopVertexID"`
	// EpochDuration is how long one epoch lasts, in nanoseconds.
	EpochDuration uint64 `json:"epochDuration"`
}

// UptimeResponse is how much of the network reports having seen this node up.
type UptimeResponse struct {
	// RewardingStakePercentage is the stake that will reward this node.
	RewardingStakePercentage float64 `json:"rewardingStakePercentage"`
	// WeightedAveragePercentage is the stake-weighted average uptime.
	WeightedAveragePercentage float64 `json:"weightedAveragePercentage"`
}

// GetTxFeeResponse is what this node charges, in nLUX.
type GetTxFeeResponse struct {
	TxFee                  uint64 `json:"txFee"`
	CreateAssetTxFee       uint64 `json:"createAssetTxFee"`
	CreateNetworkTxFee     uint64 `json:"createNetworkTxFee"`
	TransformChainTxFee    uint64 `json:"transformChainTxFee"`
	CreateChainTxFee       uint64 `json:"createChainTxFee"`
	AddNetworkValidatorFee uint64 `json:"addNetworkValidatorFee"`
	AddNetworkDelegatorFee uint64 `json:"addNetworkDelegatorFee"`
}
