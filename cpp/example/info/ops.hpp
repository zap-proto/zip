// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// The questions this node answers, and the one place each answer is written.
//
// A handler here IS the operation. Registering it (ops.cpp) yields the REST
// route, the ZAP door, the OpenAPI document, the MCP tool, the CLI command and
// the .zap schema, all from this one declaration and the doc comment above it.
//
// It is the same service examples/info is in Go, written in C++. The two must
// project to the same bytes — which is the only proof that zip is one framework
// with two front ends rather than one framework and one client.

#pragma once

#include "info/types.hpp"
#include "zip/zip.hpp"

namespace info {

/// Info answers what this node is, what network it is on, who it is connected to
/// and what its chains cost.
class Info {
  public:
    /// NodeVersion is what this node is running: its own release, the database format
    /// it reads, the VM protocol it speaks, and the consensus it is configured for.
    ///
    /// Response: {"version": "luxd/1.36.178", "databaseVersion": "v1.4.5", "rpcProtocolVersion": 39, "gitCommit": "", "vmVersions": [], "consensus": {"mode": "triple", "bls": true, "corona": true, "mlDSA": true, "platformVM": true}}
    GetNodeVersionReply nodeVersion(const zip::None& in) const;

    /// Identity is this node's id, with the proof it holds the staking key that id is
    /// derived from.
    ///
    /// Response: {"nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "nodePOP": {"publicKey": "0x8f95423f7142d00a48e1014a3de8d28907d420dc33b3052a6dee03a3f2941a393c2351e354704ca66a3fc29870282e15", "proofOfPossession": "0x86a3ab4c45cfe31cae34c1d06f212434ac71b1be6cfe046c80c162e057614a94a5bc9f1ded1a7029deb0ba4ca7c9b71411e293438691be79c2dbf19d1ca7c3eadb9c756246fc5de5b7b89511c7d7dda6"}}
    GetNodeIDReply identity(const zip::None& in) const;

    /// Address is where this node tells its peers to reach it.
    ///
    /// Response: {"ip": "203.0.113.9:9651"}
    GetNodeIPReply address(const zip::None& in) const;

    /// NetworkID is the number of the network this node is on.
    ///
    /// Response: {"networkID": 96369}
    GetNetworkIDReply networkID(const zip::None& in) const;

    /// NetworkName is the name of the network this node is on.
    ///
    /// Response: {"networkName": "mainnet"}
    GetNetworkNameReply networkName(const zip::None& in) const;

    /// ChainID resolves a chain's alias to the id it names.
    ///
    /// Example: {"alias": "X"}
    /// Response: {"blockchainID": "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM"}
    GetBlockchainIDReply chainID(const GetBlockchainIDArgs& in) const;

    /// Bootstrapped reports whether a chain has finished bootstrapping on this node.
    ///
    /// Example: {"chain": "X"}
    /// Response: {"isBootstrapped": true}
    IsBootstrappedResponse bootstrapped(const IsBootstrappedArgs& in) const;

    /// Chains are the chains this node is running, which is a subset of the chains
    /// the P-Chain knows about — platform.getBlockchains answers for those.
    ///
    /// Response: {"chains": [{"id": "11111111111111111111111111111111LpoYY", "name": "P-Chain", "vmID": "11111111111111111111111111111111LpoYY", "bootstrapped": true}]}
    GetChainsReply chains(const zip::None& in) const;

    /// Peers are the nodes this one is connected to. A list of node ids narrows the
    /// answer to those, and no list asks for all of them.
    ///
    /// Example: {"nodeIDs": []}
    /// Response: {"numPeers": 1, "peers": [{"ip": "203.0.113.9:9651", "publicIP": "203.0.113.9:9651", "nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "version": "luxd/1.36.178", "lastSent": "2026-08-29T00:00:00Z", "lastReceived": "2026-08-29T00:00:00Z", "observedUptime": 99, "trackedChains": [], "supportedLPs": [], "objectedLPs": [], "benched": []}]}
    PeersReply peers(const PeersArgs& in) const;

    /// LPs is where the network's stake stands on every current Lux Proposal.
    ///
    /// Response: {"lps": [{"number": 23, "lp": {"supportWeight": 0, "supporters": [], "objectWeight": 0, "objectors": [], "abstainWeight": 1000000000000}}]}
    LPsReply lps(const zip::None& in) const;

    /// VMs are the virtual machines installed on this node, and the feature
    /// extensions its UTXO chains understand.
    ///
    /// Response: {"vms": [{"vm": "mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6", "aliases": ["platformvm"]}], "fxs": [{"fx": "spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY", "name": "secp256k1fx"}]}
    GetVMsReply vms(const zip::None& in) const;

    /// Upgrades is the upgrade schedule this node runs.
    ///
    /// Response: {"xChainStopVertexID": "jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga", "epochDuration": 300000000000}
    UpgradeConfig upgrades(const zip::None& in) const;

    /// Uptime is how much of the network, by stake, reports having seen this node up.
    ///
    /// Response: {"rewardingStakePercentage": 100, "weightedAveragePercentage": 99.9999}
    UptimeResponse uptime(const zip::None& in) const;

    /// Fees are the transaction fees this node charges, in nLUX. Deprecated: the
    /// P-Chain's fees are dynamic and platform.getFeeConfig is the live answer.
    ///
    /// Response: {"txFee": 1000000, "createAssetTxFee": 10000000, "createNetworkTxFee": 1000000000, "transformChainTxFee": 10000000000, "createChainTxFee": 1000000000, "addNetworkValidatorFee": 1000000, "addNetworkDelegatorFee": 1000000}
    GetTxFeeResponse fees(const zip::None& in) const;
};

}  // namespace info
