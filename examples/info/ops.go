// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

// The questions this node answers, and the one place each answer is written.
//
// A handler here IS the operation. Registering it yields the REST route, the
// OpenAPI document, the MCP tool, the CLI command, the by-name call plane and
// the ZAP schema, all from this one registration and the doc comment above it.
//
// It is a transcription of the info service Lux nodes run
// (github.com/luxfi/node/service/info), kept here because the node links zip and
// zip cannot link the node. What it is FOR is the other half of the corpus:
// cpp/example/info is the same service written in C++, and the two must project
// to the same bytes — which is the only proof that zip is one framework with two
// front ends rather than one framework and one client.
package info

import (
	"context"

	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Info answers what this node is, what network it is on, who it is connected to
// and what its chains cost.
type Info struct{}

// Ops is this service's typed operations. The paths are relative to where the
// app is mounted, which the node decides — a service does not name its own
// address.
func (i *Info) Ops() *zip.App {
	app := zip.New(zip.Config{
		AppName:               "info",
		DisableStartupMessage: true,
		OpenAPI: zip.OpenAPIConfig{
			Title:       "Lux node info",
			Description: "What a Lux node tells anyone who asks: what it is running, what network it is on, who it is connected to, and what its chains cost.",
			Version:     "v1.36.178",
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
// it reads, the VM protocol it speaks, and the consensus it is configured for.
//
// Response: {"version": "luxd/1.36.178", "databaseVersion": "v1.4.5", "rpcProtocolVersion": 39, "gitCommit": "", "vmVersions": [], "consensus": {"mode": "triple", "bls": true, "corona": true, "mlDSA": true, "platformVM": true}}
func (i *Info) nodeVersion(_ context.Context, _ *struct{}) (*GetNodeVersionReply, error) {
	return &GetNodeVersionReply{
		Version:            "luxd/1.36.178",
		DatabaseVersion:    "v1.4.5",
		RPCProtocolVersion: 39,
		VMVersions:         []VMVersion{},
		Consensus:          ConsensusInfo{Mode: "triple", BLS: true, Corona: true, MLDSA: true, PlatformVM: true},
	}, nil
}

// Identity is this node's id, with the proof it holds the staking key that id is
// derived from.
//
// Response: {"nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "nodePOP": {"publicKey": "0x8f95423f7142d00a48e1014a3de8d28907d420dc33b3052a6dee03a3f2941a393c2351e354704ca66a3fc29870282e15", "proofOfPossession": "0x86a3ab4c45cfe31cae34c1d06f212434ac71b1be6cfe046c80c162e057614a94a5bc9f1ded1a7029deb0ba4ca7c9b71411e293438691be79c2dbf19d1ca7c3eadb9c756246fc5de5b7b89511c7d7dda6"}}
func (i *Info) identity(_ context.Context, _ *struct{}) (*GetNodeIDReply, error) {
	return &GetNodeIDReply{
		NodeID: "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg",
		NodePOP: ProofOfPossession{
			PublicKey:         "0x8f95423f7142d00a48e1014a3de8d28907d420dc33b3052a6dee03a3f2941a393c2351e354704ca66a3fc29870282e15",
			ProofOfPossession: "0x86a3ab4c45cfe31cae34c1d06f212434ac71b1be6cfe046c80c162e057614a94a5bc9f1ded1a7029deb0ba4ca7c9b71411e293438691be79c2dbf19d1ca7c3eadb9c756246fc5de5b7b89511c7d7dda6",
		},
	}, nil
}

// Address is where this node tells its peers to reach it.
//
// Response: {"ip": "203.0.113.9:9651"}
func (i *Info) address(_ context.Context, _ *struct{}) (*GetNodeIPReply, error) {
	return &GetNodeIPReply{IP: "203.0.113.9:9651"}, nil
}

// NetworkID is the number of the network this node is on.
//
// Response: {"networkID": 96369}
func (i *Info) networkID(_ context.Context, _ *struct{}) (*GetNetworkIDReply, error) {
	return &GetNetworkIDReply{NetworkID: 96369}, nil
}

// NetworkName is the name of the network this node is on.
//
// Response: {"networkName": "mainnet"}
func (i *Info) networkName(_ context.Context, _ *struct{}) (*GetNetworkNameReply, error) {
	return &GetNetworkNameReply{NetworkName: "mainnet"}, nil
}

// ChainID resolves a chain's alias to the id it names.
//
// Example: {"alias": "X"}
// Response: {"blockchainID": "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM"}
func (i *Info) chainID(_ context.Context, in *GetBlockchainIDArgs) (*GetBlockchainIDReply, error) {
	if in.Alias == "" {
		return nil, zip.Errorf(400, "argument 'alias' not given")
	}
	return &GetBlockchainIDReply{BlockchainID: "2oYMBNV4eNHyqk2fjjV5nVQLDbtmNJzq5s3qs3Lo6ftnC6FByM"}, nil
}

// Bootstrapped reports whether a chain has finished bootstrapping on this node.
//
// Example: {"chain": "X"}
// Response: {"isBootstrapped": true}
func (i *Info) bootstrapped(_ context.Context, in *IsBootstrappedArgs) (*IsBootstrappedResponse, error) {
	if in.Chain == "" {
		return nil, zip.Errorf(400, "argument 'chain' not given")
	}
	return &IsBootstrappedResponse{IsBootstrapped: true}, nil
}

// Chains are the chains this node is running, which is a subset of the chains
// the P-Chain knows about — platform.getBlockchains answers for those.
//
// Response: {"chains": [{"id": "11111111111111111111111111111111LpoYY", "name": "P-Chain", "vmID": "11111111111111111111111111111111LpoYY", "bootstrapped": true}]}
func (i *Info) chains(_ context.Context, _ *struct{}) (*GetChainsReply, error) {
	return &GetChainsReply{Chains: []ChainInfo{{
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
// Response: {"numPeers": 1, "peers": [{"ip": "203.0.113.9:9651", "publicIP": "203.0.113.9:9651", "nodeID": "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg", "version": "luxd/1.36.178", "lastSent": "2026-08-29T00:00:00Z", "lastReceived": "2026-08-29T00:00:00Z", "observedUptime": 99, "trackedChains": [], "supportedLPs": [], "objectedLPs": [], "benched": []}]}
func (i *Info) peers(_ context.Context, in *PeersArgs) (*PeersReply, error) {
	peers := []Peer{{
		PeerInfo: PeerInfo{
			IP:             "203.0.113.9:9651",
			PublicIP:       "203.0.113.9:9651",
			ID:             "NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg",
			Version:        "luxd/1.36.178",
			LastSent:       "2026-08-29T00:00:00Z",
			LastReceived:   "2026-08-29T00:00:00Z",
			ObservedUptime: 99,
			TrackedChains:  []string{},
			SupportedLPs:   []uint32{},
			ObjectedLPs:    []uint32{},
		},
		Benched: []string{},
	}}
	return &PeersReply{NumPeers: uint64(len(peers)), Peers: peers}, nil
}

// LPs is where the network's stake stands on every current Lux Proposal.
//
// Response: {"lps": [{"number": 23, "lp": {"supportWeight": 0, "supporters": [], "objectWeight": 0, "objectors": [], "abstainWeight": 1000000000000}}]}
func (i *Info) lps(_ context.Context, _ *struct{}) (*LPsReply, error) {
	return &LPsReply{LPs: []LPStatus{{
		Number: 23,
		LP: LP{
			Supporters:    []string{},
			Objectors:     []string{},
			AbstainWeight: 1000000000000,
		},
	}}}, nil
}

// VMs are the virtual machines installed on this node, and the feature
// extensions its UTXO chains understand.
//
// Response: {"vms": [{"vm": "mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6", "aliases": ["platformvm"]}], "fxs": [{"fx": "spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY", "name": "secp256k1fx"}]}
func (i *Info) vms(_ context.Context, _ *struct{}) (*GetVMsReply, error) {
	return &GetVMsReply{
		VMs: []VMAlias{{VM: "mgj786NP7uDwBCcq6YwThhaN8FLyybkCa4zBWTQbNgmK6k9A6", Aliases: []string{"platformvm"}}},
		Fxs: []FxName{{Fx: "spqBHsy2UBGpjaQJezEywUy1AB98eVjJ3WQ38x1vpjsx4xPkY", Name: "secp256k1fx"}},
	}, nil
}

// Upgrades is the upgrade schedule this node runs.
//
// Response: {"xChainStopVertexID": "jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga", "epochDuration": 300000000000}
func (i *Info) upgrades(_ context.Context, _ *struct{}) (*UpgradeConfig, error) {
	return &UpgradeConfig{
		XChainStopVertexID: "jrGWDh5Po9FMj54depyunNixpia5PN4aAYxfmNzU8n752Rjga",
		EpochDuration:      300000000000,
	}, nil
}

// Uptime is how much of the network, by stake, reports having seen this node up.
//
// Response: {"rewardingStakePercentage": 100, "weightedAveragePercentage": 99.9999}
func (i *Info) uptime(_ context.Context, _ *struct{}) (*UptimeResponse, error) {
	return &UptimeResponse{RewardingStakePercentage: 100, WeightedAveragePercentage: 99.9999}, nil
}

// Fees are the transaction fees this node charges, in nLUX. Deprecated: the
// P-Chain's fees are dynamic and platform.getFeeConfig is the live answer.
//
// Response: {"txFee": 1000000, "createAssetTxFee": 10000000, "createNetworkTxFee": 1000000000, "transformChainTxFee": 10000000000, "createChainTxFee": 1000000000, "addNetworkValidatorFee": 1000000, "addNetworkDelegatorFee": 1000000}
func (i *Info) fees(_ context.Context, _ *struct{}) (*GetTxFeeResponse, error) {
	return &GetTxFeeResponse{
		TxFee:                  1000000,
		CreateAssetTxFee:       10000000,
		CreateNetworkTxFee:     1000000000,
		TransformChainTxFee:    10000000000,
		CreateChainTxFee:       1000000000,
		AddNetworkValidatorFee: 1000000,
		AddNetworkDelegatorFee: 1000000,
	}, nil
}
