// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: BSD-3-Clause-Eco
//
// What each answer is, and where each question is asked.
//
// This is the file zipc reads. Every projection — the document, the tool list,
// the CLI, the schema — is a projection of the registrations below and the doc
// comments in ops.hpp, and none of them is written by hand.

#include "info/ops.hpp"

#include "info.zip.hpp"

namespace info {

GetNodeVersionReply Info::nodeVersion(const zip::None&) const {
    GetNodeVersionReply out;
    out.Version = "luxd/1.36.178";
    out.DatabaseVersion = "v1.4.5";
    out.RPCProtocolVersion = 39;
    out.Consensus = ConsensusInfo{"triple", true, true, true, true};
    return out;
}

GetNodeIDReply Info::identity(const zip::None&) const {
    GetNodeIDReply out;
    out.NodeID = "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg";
    out.NodePOP.PublicKey =
        "0x8f95423f7142d00a48e1014a3de8d28907d420dc33b3052a6dee03a3f2941a393c2351e354704ca66a3fc298"
        "70282e15";
    out.NodePOP.ProofOfPossession_ =
        "0x86a3ab4c45cfe31cae34c1d06f212434ac71b1be6cfe046c80c162e057614a94a5bc9f1ded1a7029deb0ba4c"
        "a7c9b71411e293438691be79c2dbf19d1ca7c3eadb9c756246fc5de5b7b89511c7d7dda6";
    return out;
}

GetNodeIPReply Info::address(const zip::None&) const { return GetNodeIPReply{"203.0.113.9:9651"}; }

GetNetworkIDReply Info::networkID(const zip::None&) const { return GetNetworkIDReply{96369}; }

GetNetworkNameReply Info::networkName(const zip::None&) const {
    return GetNetworkNameReply{"mainnet"};
}

GetBlockchainIDReply Info::chainID(const GetBlockchainIDArgs& in) const {
    if (in.Alias.empty()) throw zip::Error(400, "argument 'alias' not given");
    return GetBlockchainIDReply{"2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM"};
}

IsBootstrappedResponse Info::bootstrapped(const IsBootstrappedArgs& in) const {
    if (in.Chain.empty()) throw zip::Error(400, "argument 'chain' not given");
    return IsBootstrappedResponse{true};
}

GetChainsReply Info::chains(const zip::None&) const {
    GetChainsReply out;
    out.Chains.push_back(ChainInfo{"11111111111111111111111111111111LpoYY", "P-Chain",
                                   "11111111111111111111111111111111LpoYY", true});
    return out;
}

PeersReply Info::peers(const PeersArgs&) const {
    Peer peer;
    peer.IP = "203.0.113.9:9651";
    peer.PublicIP = "203.0.113.9:9651";
    peer.ID = "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg";
    peer.Version = "luxd/1.36.178";
    peer.LastSent = "2026-08-29T00:00:00Z";
    peer.LastReceived = "2026-08-29T00:00:00Z";
    peer.ObservedUptime = 99;

    PeersReply out;
    out.Peers.push_back(peer);
    out.NumPeers = out.Peers.size();
    return out;
}

LPsReply Info::lps(const zip::None&) const {
    LPStatus lp;
    lp.Number = 23;
    lp.LP.AbstainWeight = 1000000000000;

    LPsReply out;
    out.LPs.push_back(lp);
    return out;
}

GetVMsReply Info::vms(const zip::None&) const {
    GetVMsReply out;
    out.VMs.push_back(VMAlias{"mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6", {"platformvm"}});
    out.Fxs.push_back(FxName{"spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY", "secp256k1fx"});
    return out;
}

UpgradeConfig Info::upgrades(const zip::None&) const {
    return UpgradeConfig{"jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga", 300000000000};
}

UptimeResponse Info::uptime(const zip::None&) const { return UptimeResponse{100, 99.9999}; }

GetTxFeeResponse Info::fees(const zip::None&) const {
    GetTxFeeResponse out;
    out.TxFee = 1000000;
    out.CreateAssetTxFee = 10000000;
    out.CreateNetworkTxFee = 1000000000;
    out.TransformChainTxFee = 10000000000;
    out.CreateChainTxFee = 1000000000;
    out.AddNetworkValidatorFee = 1000000;
    out.AddNetworkDelegatorFee = 1000000;
    return out;
}

}  // namespace info

/// Lux node info
///
/// What a Lux node tells anyone who asks: what it is running, what network it is on, who it is connected to, and what its chains cost.
zip::App app("info");

// The registrations. Each states one op: its method, its address, and the
// handler that answers it — the same three facts zip.Get states in Go, and the
// whole of what a projection is a projection of.
void ops(info::Info* i) {
    zip::get(app, "/node/version", &info::Info::nodeVersion, i);
    zip::get(app, "/node/id", &info::Info::identity, i);
    zip::get(app, "/node/ip", &info::Info::address, i);
    zip::get(app, "/network/id", &info::Info::networkID, i);
    zip::get(app, "/network/name", &info::Info::networkName, i);
    zip::get(app, "/chain/id", &info::Info::chainID, i);
    zip::get(app, "/chain/bootstrapped", &info::Info::bootstrapped, i);
    zip::get(app, "/chains", &info::Info::chains, i);
    zip::get(app, "/peers", &info::Info::peers, i);
    zip::get(app, "/lps", &info::Info::lps, i);
    zip::get(app, "/vms", &info::Info::vms, i);
    zip::get(app, "/upgrades", &info::Info::upgrades, i);
    zip::get(app, "/uptime", &info::Info::uptime, i);
    zip::get(app, "/fees", &info::Info::fees, i);
}
