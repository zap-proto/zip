# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package info

struct Bootstrapped {
    is_bootstrapped bool @0
}

struct BootstrappedArgs {
    chain text @0
}

struct ChainAlias {
    alias text @0
}

struct ChainID {
    blockchain_id text @0
}

struct Chains {
    chains list<bytes> @0
}

struct Fees {
    tx_fee              u64 @0
    create_asset_tx_fee u64 @8
    create_chain_tx_fee u64 @16
}

struct NetworkID {
    network_id u32 @0
}

struct NetworkName {
    network_name text @0
}

struct NodeID {
    node_id  text  @0
    node_pop bytes @8
}

struct NodeIP {
    ip text @0
}

struct NodeVersion {
    version              text @0
    database_version     text @8
    rpc_protocol_version u32  @16
    git_commit           text @24
}

struct Peers {
    num_peers u64         @0
    peers     list<bytes> @8
}

struct PeersArgs {
    node_ids list<text> @0
}

struct Upgrades {
    stop_vertex_id text @0
    epoch_duration u64  @8
}

struct Uptime {
    rewarding_stake_percentage  text @0
    weighted_average_percentage text @8
}

interface info {
    # Bootstrapped reports whether a chain has finished bootstrapping on this node.
    get_chain_bootstrapped(req: BootstrappedArgs) returns (rep: Bootstrapped)
    # ChainID resolves a chain's alias to the id it names.
    get_chain_id(req: ChainAlias) returns (rep: ChainID)
    # Chains are the chains this node is running, which is a subset of the chains
    # the P-Chain knows about.
    get_chains() returns (rep: Chains)
    # Fees are the transaction fees this node charges, in nLUX.
    get_fees() returns (rep: Fees)
    # NetworkID is the number of the network this node is on.
    get_network_id() returns (rep: NetworkID)
    # NetworkName is the name of the network this node is on.
    get_network_name() returns (rep: NetworkName)
    # Identity is this node's id, with the proof it holds the staking key that id is
    # derived from.
    get_node_id() returns (rep: NodeID)
    # Address is where this node tells its peers to reach it.
    get_node_ip() returns (rep: NodeIP)
    # NodeVersion is what this node is running: its own release, the database format
    # it reads, and the VM protocol it speaks.
    get_node_version() returns (rep: NodeVersion)
    # Peers are the nodes this one is connected to. A list of node ids narrows the
    # answer to those, and no list asks for all of them.
    get_peers(req: PeersArgs) returns (rep: Peers)
    # Upgrades is the upgrade schedule this node runs.
    get_upgrades() returns (rep: Upgrades)
    # Uptime is how much of the network, by stake, reports having seen this node up.
    get_uptime() returns (rep: Uptime)
}

# ---------------------------------------------------------------------
# 12 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   get_lps  LPs.lps  HashMap<String, LP>  (map)
#   get_vms  VMs.fxs  HashMap<String, String>  (map)
#   get_vms  VMs.vms  HashMap<String, Vec<String>>  (map)
#
# opaque (3) — crosses, arrives without its name:
#   Chains.chains  Chain (list element)
#   NodeID.node_pop  ProofOfPossession
#   Peers.peers  Peer (list element)
