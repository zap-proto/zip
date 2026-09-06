// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

//! The questions this node answers, and the one place each answer is written.
//!
//! A handler here IS the operation. There is no second copy of it behind a
//! method table: writing it yields the REST route, the ZAP method, the OpenAPI
//! document, the MCP tool, the CLI command and the ZAP schema, all from this one
//! declaration and the doc comment above it.

use std::collections::HashMap;

use crate::types::*;

/// Info answers questions about one node. It holds the answers rather than
/// computing them, because this service exists to be projected, not to run a
/// network.
pub struct Info {
    pub release: String,
    pub network: u32,
}

#[zip::ops(
    app = "info",
    title = "Lux node info",
    version = "1.0.0",
    description = "What a Lux node tells anyone who asks: what it is running, what network it is on, who it is connected to, and what its chains cost."
)]
impl Info {
    /// NodeVersion is what this node is running: its own release, the database format
    /// it reads, and the VM protocol it speaks.
    ///
    /// Response: {"version": "luxd/1.36.178", "databaseVersion": "v1.4.5", "rpcProtocolVersion": 39, "gitCommit": ""}
    #[get("/node/version")]
    fn node_version(&self) -> Result<NodeVersion, zip::Error> {
        Ok(NodeVersion {
            version: self.release.clone(),
            database_version: "v1.4.5".into(),
            rpc_protocol_version: 39,
            git_commit: String::new(),
        })
    }

    /// Identity is this node's id, with the proof it holds the staking key that id is
    /// derived from.
    ///
    /// Response: {"nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "nodePOP": {"publicKey": "0x8f95", "proofOfPossession": "0x86a3"}}
    #[get("/node/id")]
    fn identity(&self) -> Result<NodeID, zip::Error> {
        Ok(NodeID {
            node_id: "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg".into(),
            node_pop: ProofOfPossession {
                public_key: "0x8f95".into(),
                proof_of_possession: "0x86a3".into(),
            },
        })
    }

    /// Address is where this node tells its peers to reach it.
    ///
    /// Response: {"ip": "203.0.113.9:9651"}
    #[get("/node/ip")]
    fn address(&self) -> Result<NodeIP, zip::Error> {
        Ok(NodeIP {
            ip: "203.0.113.9:9651".into(),
        })
    }

    /// NetworkID is the number of the network this node is on.
    ///
    /// Response: {"networkID": 96369}
    #[get("/network/id")]
    fn network_id(&self) -> Result<NetworkID, zip::Error> {
        Ok(NetworkID {
            network_id: self.network,
        })
    }

    /// NetworkName is the name of the network this node is on.
    ///
    /// Response: {"networkName": "mainnet"}
    #[get("/network/name")]
    fn network_name(&self) -> Result<NetworkName, zip::Error> {
        Ok(NetworkName {
            network_name: "mainnet".into(),
        })
    }

    /// ChainID resolves a chain's alias to the id it names.
    ///
    /// Example: {"alias": "X"}
    /// Response: {"blockchainID": "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM"}
    #[get("/chain/id")]
    fn chain_id(&self, arg: &ChainAlias) -> Result<ChainID, zip::Error> {
        if arg.alias.is_empty() {
            return Err(zip::Error::bad("argument 'alias' not given"));
        }
        Ok(ChainID {
            blockchain_id: "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM".into(),
        })
    }

    /// Bootstrapped reports whether a chain has finished bootstrapping on this node.
    ///
    /// Example: {"chain": "X"}
    /// Response: {"isBootstrapped": true}
    #[get("/chain/bootstrapped")]
    fn bootstrapped(&self, arg: &BootstrappedArgs) -> Result<Bootstrapped, zip::Error> {
        if arg.chain.is_empty() {
            return Err(zip::Error::bad("argument 'chain' not given"));
        }
        Ok(Bootstrapped {
            is_bootstrapped: true,
        })
    }

    /// Chains are the chains this node is running, which is a subset of the chains
    /// the P-Chain knows about.
    ///
    /// Response: {"chains": [{"id": "11111111111111111111111111111111LpoYY", "name": "P-Chain", "vmID": "11111111111111111111111111111111LpoYY", "bootstrapped": true}]}
    #[get("/chains")]
    fn chains(&self) -> Result<Chains, zip::Error> {
        Ok(Chains {
            chains: vec![Chain {
                id: "11111111111111111111111111111111LpoYY".into(),
                name: "P-Chain".into(),
                vm_id: "11111111111111111111111111111111LpoYY".into(),
                bootstrapped: true,
            }],
        })
    }

    /// Peers are the nodes this one is connected to. A list of node ids narrows the
    /// answer to those, and no list asks for all of them.
    ///
    /// Example: {"nodeIDs": []}
    /// Response: {"numPeers": "1", "peers": [{"ip": "203.0.113.9:9651", "publicIP": "203.0.113.9:9651", "nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "version": "luxd/1.36.178", "observedUptime": 99, "trackedChains": []}]}
    #[get("/peers")]
    fn peers(&self, _arg: &PeersArgs) -> Result<Peers, zip::Error> {
        let peers = vec![Peer {
            ip: "203.0.113.9:9651".into(),
            public_ip: "203.0.113.9:9651".into(),
            node_id: "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg".into(),
            version: "luxd/1.36.178".into(),
            observed_uptime: 99,
            tracked_chains: vec![],
        }];
        Ok(Peers {
            num_peers: Uint64(peers.len() as u64),
            peers,
        })
    }

    /// LPs is where the network's stake stands on every current Lux Proposal.
    ///
    /// Response: {"lps": {"23": {"supportWeight": "0", "objectWeight": "0", "abstainWeight": "1000000000000"}}}
    #[get("/lps")]
    fn lps(&self) -> Result<LPs, zip::Error> {
        let mut lps = HashMap::new();
        lps.insert(
            "23".to_string(),
            LP {
                abstain_weight: Uint64(1_000_000_000_000),
                ..Default::default()
            },
        );
        Ok(LPs { lps })
    }

    /// VMs are the virtual machines installed on this node, and the feature
    /// extensions its UTXO chains understand.
    ///
    /// Response: {"vms": {"mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6": ["platformvm"]}, "fxs": {"spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY": "secp256k1fx"}}
    #[get("/vms")]
    fn vms(&self) -> Result<VMs, zip::Error> {
        let mut vms = HashMap::new();
        vms.insert(
            "mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6".to_string(),
            vec!["platformvm".to_string()],
        );
        let mut fxs = HashMap::new();
        fxs.insert(
            "spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY".to_string(),
            "secp256k1fx".to_string(),
        );
        Ok(VMs { vms, fxs })
    }

    /// Upgrades is the upgrade schedule this node runs.
    ///
    /// Response: {"stopVertexID": "jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga", "epochDuration": "300000000000"}
    #[get("/upgrades")]
    fn upgrades(&self) -> Result<Upgrades, zip::Error> {
        Ok(Upgrades {
            stop_vertex_id: "jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga".into(),
            epoch_duration: Uint64(300_000_000_000),
        })
    }

    /// Uptime is how much of the network, by stake, reports having seen this node up.
    ///
    /// Response: {"rewardingStakePercentage": "100.0000", "weightedAveragePercentage": "99.9999"}
    #[get("/uptime")]
    fn uptime(&self) -> Result<Uptime, zip::Error> {
        Ok(Uptime {
            rewarding_stake_percentage: "100.0000".into(),
            weighted_average_percentage: "99.9999".into(),
        })
    }

    /// Fees are the transaction fees this node charges, in nLUX.
    ///
    /// Response: {"txFee": "1000000", "createAssetTxFee": "10000000", "createChainTxFee": "1000000000"}
    #[get("/fees")]
    fn fees(&self) -> Result<Fees, zip::Error> {
        Ok(Fees {
            tx_fee: Uint64(1_000_000),
            create_asset_tx_fee: Uint64(10_000_000),
            create_chain_tx_fee: Uint64(1_000_000_000),
        })
    }
}
