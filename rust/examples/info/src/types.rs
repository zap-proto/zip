// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! What a node tells anyone who asks — the conformance corpus, in Rust.
//!
//! This service exists three times: here, in corpus/info (Go) and in
//! cpp/examples/info. The three are written in their own languages, in their
//! own idioms, and each one's framework projects it without help from the other
//! two. The projections are then compared byte for byte.
//!
//! The shapes are chosen to be the ones that break: a quoted decimal (a value
//! whose JSON form is not what it is made of), a nested named struct, a list of
//! structs, a map, and an op that takes nothing at all.

use std::collections::HashMap;

use zip::Wire;

/// Uint64 is a 64-bit count carried as a quoted decimal, because JSON numbers
/// lose precision above 2^53 and a stake does not.
///
/// It is the case that makes the manifest carry two answers for one type: to a
/// JSON reader this is a string, and to a fixed layout it is eight bytes. A
/// description that could only say one of those would make one projection lie.
#[derive(Wire, Clone, Copy, Default, Debug)]
#[zip(text, json = r#"{"pattern":"^[0-9]+$","type":"string"}"#)]
pub struct Uint64(pub u64);

impl From<u64> for Uint64 {
    fn from(v: u64) -> Self {
        Uint64(v)
    }
}

/// NodeVersion is what this node is running.
#[derive(Wire, Default)]
pub struct NodeVersion {
    /// Version is the node's own release.
    #[zip(json = "version")]
    pub version: String,
    /// DatabaseVersion is the database format this node reads.
    #[zip(json = "databaseVersion")]
    pub database_version: String,
    /// RPCProtocolVersion is the VM protocol this node speaks.
    #[zip(json = "rpcProtocolVersion")]
    pub rpc_protocol_version: u32,
    /// GitCommit is the commit this node was built from.
    #[zip(json = "gitCommit")]
    pub git_commit: String,
}

/// NodeID is this node's id, with the proof it holds the staking key.
#[derive(Wire, Default)]
pub struct NodeID {
    /// NodeID is the id peers know this node by.
    #[zip(json = "nodeID")]
    pub node_id: String,
    /// NodePOP is the proof this node holds the key its id is derived from.
    #[zip(json = "nodePOP")]
    pub node_pop: ProofOfPossession,
}

/// ProofOfPossession is a staking key and a signature over it.
#[derive(Wire, Default)]
pub struct ProofOfPossession {
    /// PublicKey is the BLS public key, hex encoded.
    #[zip(json = "publicKey")]
    pub public_key: String,
    /// ProofOfPossession is the signature over that key, hex encoded.
    #[zip(json = "proofOfPossession")]
    pub proof_of_possession: String,
}

/// NodeIP is where this node tells its peers to reach it.
#[derive(Wire, Default)]
pub struct NodeIP {
    /// IP is the address and port peers dial.
    #[zip(json = "ip")]
    pub ip: String,
}

/// NetworkID is the number of the network a node is on.
#[derive(Wire, Default)]
pub struct NetworkID {
    /// NetworkID is the network's number.
    #[zip(json = "networkID")]
    pub network_id: u32,
}

/// NetworkName is the name of the network a node is on.
#[derive(Wire, Default)]
pub struct NetworkName {
    /// NetworkName is the network's name.
    #[zip(json = "networkName")]
    pub network_name: String,
}

/// ChainAlias names the chain a caller is asking about.
#[derive(Wire, Default)]
pub struct ChainAlias {
    /// Alias is the name the chain answers to, such as "X".
    #[zip(json = "alias", required)]
    pub alias: String,
}

/// ChainID is the id an alias names.
#[derive(Wire, Default)]
pub struct ChainID {
    /// BlockchainID is the chain's id.
    #[zip(json = "blockchainID")]
    pub blockchain_id: String,
}

/// BootstrappedArgs names the chain whose bootstrap state is being asked about.
#[derive(Wire, Default)]
pub struct BootstrappedArgs {
    /// Chain is the alias or id of the chain to ask about.
    #[zip(json = "chain", required)]
    pub chain: String,
}

/// Bootstrapped is whether a chain has finished bootstrapping.
#[derive(Wire, Default)]
pub struct Bootstrapped {
    /// IsBootstrapped is true once the chain has caught up.
    #[zip(json = "isBootstrapped")]
    pub is_bootstrapped: bool,
}

/// Chain is one chain this node runs.
#[derive(Wire, Default)]
pub struct Chain {
    /// ID is the chain's id.
    #[zip(json = "id")]
    pub id: String,
    /// Name is the chain's name.
    #[zip(json = "name")]
    pub name: String,
    /// VMID is the id of the virtual machine the chain runs.
    #[zip(json = "vmID")]
    pub vm_id: String,
    /// Bootstrapped is whether this node has caught the chain up.
    #[zip(json = "bootstrapped")]
    pub bootstrapped: bool,
}

/// Chains are the chains this node runs.
#[derive(Wire, Default)]
pub struct Chains {
    /// Chains are the chains this node is running.
    #[zip(json = "chains")]
    pub chains: Vec<Chain>,
}

/// PeersArgs narrows the peer list to the nodes named.
#[derive(Wire, Default)]
pub struct PeersArgs {
    /// NodeIDs are the peers to answer for; none asks for all of them.
    #[zip(json = "nodeIDs")]
    pub node_ids: Vec<String>,
}

/// Peer is one connection this node holds.
#[derive(Wire, Default)]
pub struct Peer {
    /// IP is the address this node reaches the peer at.
    #[zip(json = "ip")]
    pub ip: String,
    /// PublicIP is the address the peer tells others to use.
    #[zip(json = "publicIP")]
    pub public_ip: String,
    /// NodeID is the peer's id.
    #[zip(json = "nodeID")]
    pub node_id: String,
    /// Version is the release the peer is running.
    #[zip(json = "version")]
    pub version: String,
    /// ObservedUptime is the share of time this node has seen the peer up.
    #[zip(json = "observedUptime")]
    pub observed_uptime: u32,
    /// TrackedChains are the chains the peer is following.
    #[zip(json = "trackedChains")]
    pub tracked_chains: Vec<String>,
}

/// Peers are the nodes this one is connected to.
#[derive(Wire, Default)]
pub struct Peers {
    /// NumPeers is how many connections this node holds.
    #[zip(json = "numPeers")]
    pub num_peers: Uint64,
    /// Peers are the connections themselves.
    #[zip(json = "peers")]
    pub peers: Vec<Peer>,
}

/// LP is where the stake stands on one proposal.
#[derive(Wire, Default)]
pub struct LP {
    /// SupportWeight is the stake in favour.
    #[zip(json = "supportWeight")]
    pub support_weight: Uint64,
    /// ObjectWeight is the stake against.
    #[zip(json = "objectWeight")]
    pub object_weight: Uint64,
    /// AbstainWeight is the stake that has not said.
    #[zip(json = "abstainWeight")]
    pub abstain_weight: Uint64,
}

/// LPs is where the network's stake stands on every current proposal.
#[derive(Wire, Default)]
pub struct LPs {
    /// LPs are the proposals, by number.
    #[zip(json = "lps")]
    pub lps: HashMap<String, LP>,
}

/// VMs are the virtual machines installed on this node.
#[derive(Wire, Default)]
pub struct VMs {
    /// VMs are the installed machines, by id, each with the names it answers to.
    #[zip(json = "vms")]
    pub vms: HashMap<String, Vec<String>>,
    /// Fxs are the feature extensions the UTXO chains understand, by id.
    #[zip(json = "fxs")]
    pub fxs: HashMap<String, String>,
}

/// Upgrades is the upgrade schedule this node runs.
#[derive(Wire, Default)]
pub struct Upgrades {
    /// StopVertexID is the vertex the linearisation stopped at.
    #[zip(json = "stopVertexID")]
    pub stop_vertex_id: String,
    /// EpochDuration is how long one epoch lasts, in nanoseconds.
    #[zip(json = "epochDuration")]
    pub epoch_duration: Uint64,
}

/// Uptime is how much of the network reports having seen this node up.
#[derive(Wire, Default)]
pub struct Uptime {
    /// RewardingStakePercentage is the share of stake that would reward this node.
    #[zip(json = "rewardingStakePercentage")]
    pub rewarding_stake_percentage: String,
    /// WeightedAveragePercentage is the stake-weighted average uptime.
    #[zip(json = "weightedAveragePercentage")]
    pub weighted_average_percentage: String,
}

/// Fees are the transaction fees this node charges, in nLUX.
#[derive(Wire, Default)]
pub struct Fees {
    /// TxFee is the fee for an ordinary transaction.
    #[zip(json = "txFee")]
    pub tx_fee: Uint64,
    /// CreateAssetTxFee is the fee to create an asset.
    #[zip(json = "createAssetTxFee")]
    pub create_asset_tx_fee: Uint64,
    /// CreateChainTxFee is the fee to create a chain.
    #[zip(json = "createChainTxFee")]
    pub create_chain_tx_fee: Uint64,
}
