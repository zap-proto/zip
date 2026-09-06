// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// What a node says about itself, as the wire carries it.
//
// The same shapes examples/info/types.go declares, in C++. Nothing here is
// generated and nothing here is registered: these are the plain types the
// handlers take and return, and zipc reads them out of this file with the
// compiler's own parser to write the manifest every projection comes from.
//
// ZIP_JSON names the wire's spelling where it differs from the member's, which
// is the same thing a `json:` tag does in the Go declaration beside it.

#pragma once

#include <cstdint>
#include <string>
#include <vector>

#include "zip/mark.hpp"

namespace info {

/// VMVersion is one VM and the version of it a node runs.
struct VMVersion {
    /// VM is the VM's alias.
    ZIP_JSON("vm") std::string VM;
    /// Version is the release of it this node runs.
    ZIP_JSON("version") std::string Version;
};

/// ConsensusInfo summarises the consensus configuration of a running node, so a
/// caller does not have to scrape the boot logs to learn what it is running.
struct ConsensusInfo {
    /// Mode is "triple" (BLS + Corona + ML-DSA), "dual" (BLS + Corona), or
    /// "classical" (BLS only).
    ZIP_JSON("mode") std::string Mode;
    /// BLS is always true for a production Quasar node.
    ZIP_JSON("bls") bool BLS = false;
    /// Corona is true when the post-quantum lattice threshold path is wired.
    ZIP_JSON("corona") bool Corona = false;
    /// MLDSA is true when ML-DSA-65 signature verification is wired.
    ZIP_JSON("mlDSA") bool MLDSA = false;
    /// PlatformVM is true when the production PlatformVM wiring is in use.
    ZIP_JSON("platformVM") bool PlatformVM = false;
};

/// GetNodeVersionReply is what this node is running.
struct GetNodeVersionReply {
    ZIP_JSON("version") std::string Version;
    ZIP_JSON("databaseVersion") std::string DatabaseVersion;
    ZIP_JSON("rpcProtocolVersion") std::uint32_t RPCProtocolVersion = 0;
    ZIP_JSON("gitCommit") std::string GitCommit;
    ZIP_JSON("vmVersions") std::vector<VMVersion> VMVersions;
    ZIP_JSON("consensus") ConsensusInfo Consensus;
};

/// ProofOfPossession is a BLS proof of possession, hex-encoded.
struct ProofOfPossession {
    ZIP_JSON("publicKey") std::string PublicKey;
    ZIP_JSON("proofOfPossession") std::string ProofOfPossession_;
};

/// GetNodeIDReply is this node's id and the proof it holds the staking key.
struct GetNodeIDReply {
    ZIP_JSON("nodeID") std::string NodeID;
    ZIP_JSON("nodePOP") ProofOfPossession NodePOP;
};

/// GetNodeIPReply is where this node tells its peers to reach it.
struct GetNodeIPReply {
    ZIP_JSON("ip") std::string IP;
};

/// GetNetworkIDReply is the number of the network this node is on.
struct GetNetworkIDReply {
    ZIP_JSON("networkID") std::uint32_t NetworkID = 0;
};

/// GetNetworkNameReply is the name of the network this node is on.
struct GetNetworkNameReply {
    ZIP_JSON("networkName") std::string NetworkName;
};

/// GetBlockchainIDArgs names a chain by an alias.
struct GetBlockchainIDArgs {
    /// Alias is the name the chain answers to, such as "X".
    ZIP_JSON("alias") ZIP_REQUIRED std::string Alias;
};

/// GetBlockchainIDReply is the id the alias names.
struct GetBlockchainIDReply {
    ZIP_JSON("blockchainID") std::string BlockchainID;
};

/// IsBootstrappedArgs names the chain to ask about.
struct IsBootstrappedArgs {
    /// Chain is the alias or id of the chain to ask about.
    ZIP_JSON("chain") ZIP_REQUIRED std::string Chain;
};

/// IsBootstrappedResponse is whether that chain has finished.
struct IsBootstrappedResponse {
    ZIP_JSON("isBootstrapped") bool IsBootstrapped = false;
};

/// ChainInfo is one chain this node runs.
struct ChainInfo {
    /// ID is the chain's id.
    ZIP_JSON("id") std::string ID;
    /// Name is what it is called.
    ZIP_JSON("name") std::string Name;
    /// VMID is the VM it runs.
    ZIP_JSON("vmID") std::string VMID;
    /// Bootstrapped is whether it has finished bootstrapping here.
    ZIP_JSON("bootstrapped") bool Bootstrapped = false;
};

/// GetChainsReply is the chains this node is running.
struct GetChainsReply {
    /// Chains are the chains this node is running.
    ZIP_JSON("chains") std::vector<ChainInfo> Chains;
};

/// PeersArgs narrows the answer to the peers named.
struct PeersArgs {
    /// NodeIDs are the peers to answer for; none asks for all of them.
    ZIP_JSON("nodeIDs") std::vector<std::string> NodeIDs;
};

/// PeerInfo is one live connection as the wire carries it.
struct PeerInfo {
    /// IP is the address this node reaches the peer at.
    ZIP_JSON("ip") std::string IP;
    /// PublicIP is the address the peer says it is reachable at.
    ZIP_JSON("publicIP") std::string PublicIP;
    /// ID is the peer's node id.
    ZIP_JSON("nodeID") std::string ID;
    /// Version is the node software the peer is running.
    ZIP_JSON("version") std::string Version;
    /// LastSent is when this node last sent the peer a message.
    ZIP_JSON("lastSent") std::string LastSent;
    /// LastReceived is when this node last heard from the peer.
    ZIP_JSON("lastReceived") std::string LastReceived;
    /// ObservedUptime is the percentage of time this node has seen the peer up.
    ZIP_JSON("observedUptime") std::uint32_t ObservedUptime = 0;
    /// TrackedChains are the chains the peer serves, ordered by id.
    ZIP_JSON("trackedChains") std::vector<std::string> TrackedChains;
    /// SupportedLPs are the LPs the peer votes for, ascending.
    ZIP_JSON("supportedLPs") std::vector<std::uint32_t> SupportedLPs;
    /// ObjectedLPs are the LPs the peer votes against, ascending.
    ZIP_JSON("objectedLPs") std::vector<std::uint32_t> ObjectedLPs;
};

/// Peer is a connection, with what this node has benched it for.
struct Peer : PeerInfo {
    /// Benched are the chains this node has stopped talking to it about.
    ZIP_JSON("benched") std::vector<std::string> Benched;
};

/// PeersReply is who this node is connected to.
struct PeersReply {
    /// NumPeers is how many there are.
    ZIP_JSON("numPeers") std::uint64_t NumPeers = 0;
    /// Peers are the connections themselves.
    ZIP_JSON("peers") std::vector<Peer> Peers;
};

/// LP is the stake for, against and abstaining on one proposal.
struct LP {
    /// SupportWeight is the stake voting for it.
    ZIP_JSON("supportWeight") std::uint64_t SupportWeight = 0;
    /// Supporters are the peers voting for it, ordered by node id.
    ZIP_JSON("supporters") std::vector<std::string> Supporters;
    /// ObjectWeight is the stake voting against it.
    ZIP_JSON("objectWeight") std::uint64_t ObjectWeight = 0;
    /// Objectors are the peers voting against it, ordered by node id.
    ZIP_JSON("objectors") std::vector<std::string> Objectors;
    /// AbstainWeight is the stake that has said nothing.
    ZIP_JSON("abstainWeight") std::uint64_t AbstainWeight = 0;
};

/// LPStatus is one proposal and where the stake stands on it.
struct LPStatus {
    /// Number is the LP's number, the key it appears under on the JSON wire.
    ZIP_JSON("number") std::uint32_t Number = 0;
    /// LP is the stake for, against and abstaining.
    ZIP_JSON("lp") info::LP LP;
};

/// LPsReply is where the stake stands on every current proposal.
struct LPsReply {
    /// LPs are the proposals, ascending by number.
    ZIP_JSON("lps") std::vector<LPStatus> LPs;
};

/// VMAlias is one VM installed on this node.
struct VMAlias {
    /// VM is the VM's id.
    ZIP_JSON("vm") std::string VM;
    /// Aliases are the names it also answers to.
    ZIP_JSON("aliases") std::vector<std::string> Aliases;
};

/// FxName is one feature extension the UTXO chains understand.
struct FxName {
    /// Fx is the extension's id.
    ZIP_JSON("fx") std::string Fx;
    /// Name is what it is called.
    ZIP_JSON("name") std::string Name;
};

/// GetVMsReply is what is installed on this node.
struct GetVMsReply {
    /// VMs are the virtual machines installed here.
    ZIP_JSON("vms") std::vector<VMAlias> VMs;
    /// Fxs are the feature extensions the UTXO chains understand.
    ZIP_JSON("fxs") std::vector<FxName> Fxs;
};

/// UpgradeConfig is the upgrade schedule this node runs.
struct UpgradeConfig {
    /// XChainStopVertexID is where the X-Chain stopped being a DAG.
    ZIP_JSON("xChainStopVertexID") std::string XChainStopVertexID;
    /// EpochDuration is how long one epoch lasts, in nanoseconds.
    ZIP_JSON("epochDuration") std::uint64_t EpochDuration = 0;
};

/// UptimeResponse is how much of the network reports having seen this node up.
struct UptimeResponse {
    /// RewardingStakePercentage is the stake that will reward this node.
    ZIP_JSON("rewardingStakePercentage") double RewardingStakePercentage = 0;
    /// WeightedAveragePercentage is the stake-weighted average uptime.
    ZIP_JSON("weightedAveragePercentage") double WeightedAveragePercentage = 0;
};

/// GetTxFeeResponse is what this node charges, in nLUX.
struct GetTxFeeResponse {
    ZIP_JSON("txFee") std::uint64_t TxFee = 0;
    ZIP_JSON("createAssetTxFee") std::uint64_t CreateAssetTxFee = 0;
    ZIP_JSON("createNetworkTxFee") std::uint64_t CreateNetworkTxFee = 0;
    ZIP_JSON("transformChainTxFee") std::uint64_t TransformChainTxFee = 0;
    ZIP_JSON("createChainTxFee") std::uint64_t CreateChainTxFee = 0;
    ZIP_JSON("addNetworkValidatorFee") std::uint64_t AddNetworkValidatorFee = 0;
    ZIP_JSON("addNetworkDelegatorFee") std::uint64_t AddNetworkDelegatorFee = 0;
};

}  // namespace info
