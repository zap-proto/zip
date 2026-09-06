// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package info

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Info answers questions about one node. It holds the answers rather than
// computing them, because this service exists to be projected, not to run a
// network.
type Info struct {
	Release string
	Network uint32
}

// Ops is this service's typed operations. Registering a handler yields the REST
// route, the OpenAPI document, the MCP tool, the CLI command and the ZAP method,
// all from this one registration and the doc comment above it.
func (i *Info) Ops() *zip.App {
	app := zip.New(zip.Config{
		AppName:               "info",
		DisableStartupMessage: true,
		OpenAPI: zip.OpenAPIConfig{
			Title:       "Lux node info",
			Description: "What a Lux node tells anyone who asks: what it is running, what network it is on, who it is connected to, and what its chains cost.",
			Version:     "1.0.0",
		},
	})
	zip.Get(app, "/node/version", i.nodeVersion)
	zip.Get(app, "/node/id", i.identity)
	zip.Get(app, "/node/ip", i.address)
	zip.Get(app, "/network/id", i.networkID)
	zip.Get(app, "/network/name", i.networkName)
	zip.Get(app, "/chain/id", i.chainID)
	zip.Get(app, "/chain/bootstrapped", i.bootstrapped)
	zip.Get(app, "/chains", i.chains)
	zip.Get(app, "/peers", i.peers)
	zip.Get(app, "/lps", i.lps)
	zip.Get(app, "/vms", i.vms)
	zip.Get(app, "/upgrades", i.upgrades)
	zip.Get(app, "/uptime", i.uptime)
	zip.Get(app, "/fees", i.fees)
	return app
}

// NodeVersion is what this node is running: its own release, the database format
// it reads, and the VM protocol it speaks.
//
// Response: {"version": "luxd/1.36.178", "databaseVersion": "v1.4.5", "rpcProtocolVersion": 39, "gitCommit": ""}
func (i *Info) nodeVersion(context.Context, *struct{}) (*NodeVersion, error) {
	return &NodeVersion{
		Version:            i.Release,
		DatabaseVersion:    "v1.4.5",
		RPCProtocolVersion: 39,
	}, nil
}

// Identity is this node's id, with the proof it holds the staking key that id is
// derived from.
//
// Response: {"nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "nodePOP": {"publicKey": "0x8f95", "proofOfPossession": "0x86a3"}}
func (i *Info) identity(context.Context, *struct{}) (*NodeID, error) {
	return &NodeID{
		NodeID:  "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg",
		NodePOP: ProofOfPossession{PublicKey: "0x8f95", ProofOfPossession: "0x86a3"},
	}, nil
}

// Address is where this node tells its peers to reach it.
//
// Response: {"ip": "203.0.113.9:9651"}
func (i *Info) address(context.Context, *struct{}) (*NodeIP, error) {
	return &NodeIP{IP: "203.0.113.9:9651"}, nil
}

// NetworkID is the number of the network this node is on.
//
// Response: {"networkID": 96369}
func (i *Info) networkID(context.Context, *struct{}) (*NetworkID, error) {
	return &NetworkID{NetworkID: i.Network}, nil
}

// NetworkName is the name of the network this node is on.
//
// Response: {"networkName": "mainnet"}
func (i *Info) networkName(context.Context, *struct{}) (*NetworkName, error) {
	return &NetworkName{NetworkName: "mainnet"}, nil
}

// ChainID resolves a chain's alias to the id it names.
//
// Example: {"alias": "X"}
// Response: {"blockchainID": "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM"}
func (i *Info) chainID(_ context.Context, in *ChainAlias) (*ChainID, error) {
	if in.Alias == "" {
		return nil, zip.Errorf(400, "argument 'alias' not given")
	}
	return &ChainID{BlockchainID: "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM"}, nil
}

// Bootstrapped reports whether a chain has finished bootstrapping on this node.
//
// Example: {"chain": "X"}
// Response: {"isBootstrapped": true}
func (i *Info) bootstrapped(_ context.Context, in *BootstrappedArgs) (*Bootstrapped, error) {
	if in.Chain == "" {
		return nil, zip.Errorf(400, "argument 'chain' not given")
	}
	return &Bootstrapped{IsBootstrapped: true}, nil
}

// Chains are the chains this node is running, which is a subset of the chains
// the P-Chain knows about.
//
// Response: {"chains": [{"id": "11111111111111111111111111111111LpoYY", "name": "P-Chain", "vmID": "11111111111111111111111111111111LpoYY", "bootstrapped": true}]}
func (i *Info) chains(context.Context, *struct{}) (*Chains, error) {
	return &Chains{Chains: []Chain{{
		ID:           "11111111111111111111111111111111LpoYY",
		Name:         "P-Chain",
		VMID:         "11111111111111111111111111111111LpoYY",
		Bootstrapped: true,
	}}}, nil
}

// Peers are the nodes this one is connected to. A list of node ids narrows the
// answer to those, and no list asks for all of them.
//
// Example: {"nodeIDs": []}
// Response: {"numPeers": "1", "peers": [{"ip": "203.0.113.9:9651", "publicIP": "203.0.113.9:9651", "nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "version": "luxd/1.36.178", "observedUptime": 99, "trackedChains": []}]}
func (i *Info) peers(_ context.Context, in *PeersArgs) (*Peers, error) {
	peers := []Peer{{
		IP:             "203.0.113.9:9651",
		PublicIP:       "203.0.113.9:9651",
		NodeID:         "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg",
		Version:        "luxd/1.36.178",
		ObservedUptime: 99,
		TrackedChains:  []string{},
	}}
	return &Peers{NumPeers: Uint64(len(peers)), Peers: peers}, nil
}

// LPs is where the network's stake stands on every current Lux Proposal.
//
// Response: {"lps": {"23": {"supportWeight": "0", "objectWeight": "0", "abstainWeight": "1000000000000"}}}
func (i *Info) lps(context.Context, *struct{}) (*LPs, error) {
	return &LPs{LPs: map[string]LP{"23": {AbstainWeight: 1000000000000}}}, nil
}

// VMs are the virtual machines installed on this node, and the feature
// extensions its UTXO chains understand.
//
// Response: {"vms": {"mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6": ["platformvm"]}, "fxs": {"spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY": "secp256k1fx"}}
func (i *Info) vms(context.Context, *struct{}) (*VMs, error) {
	return &VMs{
		VMs: map[string][]string{"mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6": {"platformvm"}},
		Fxs: map[string]string{"spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY": "secp256k1fx"},
	}, nil
}

// Upgrades is the upgrade schedule this node runs.
//
// Response: {"stopVertexID": "jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga", "epochDuration": "300000000000"}
func (i *Info) upgrades(context.Context, *struct{}) (*Upgrades, error) {
	return &Upgrades{
		StopVertexID:  "jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga",
		EpochDuration: 300000000000,
	}, nil
}

// Uptime is how much of the network, by stake, reports having seen this node up.
//
// Response: {"rewardingStakePercentage": "100.0000", "weightedAveragePercentage": "99.9999"}
func (i *Info) uptime(context.Context, *struct{}) (*Uptime, error) {
	return &Uptime{
		RewardingStakePercentage:  "100.0000",
		WeightedAveragePercentage: "99.9999",
	}, nil
}

// Fees are the transaction fees this node charges, in nLUX.
//
// Response: {"txFee": "1000000", "createAssetTxFee": "10000000", "createChainTxFee": "1000000000"}
func (i *Info) fees(context.Context, *struct{}) (*Fees, error) {
	return &Fees{TxFee: 1000000, CreateAssetTxFee: 10000000, CreateChainTxFee: 1000000000}, nil
}
