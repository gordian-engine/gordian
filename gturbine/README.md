# GTurbine 🌪️

GTurbine is a high-performance block propagation protocol designed for distributed consensus systems. It uses a structured network topology and Reed-Solomon erasure coding to efficiently propagate blocks across large validator networks while minimizing bandwidth requirements.

*Because flooding blocks to every node is **so** 2019...*

## Overview

GTurbine implements a multi-layer propagation strategy inspired by Solana's Turbine protocol. Rather than having proposers flood blocks to every validator, GTurbine orchestrates a structured propagation flow:

1. **Block Creation** (Proposer):
   - Proposer reaps transactions from mempool
   - Assembles them into a new block
   - Calculates block header and metadata

2. **Block Shredding** (Proposer):
   - Splits block into fixed-size data shreds
   - Applies Reed-Solomon erasure coding
   - Generates recovery shreds for fault tolerance
   - Tags all shreds with unique group ID and metadata

3. **Tree Organization** (Network-wide):
   - Validators self-organize into propagation layers
   - Each validator knows its upstream source and downstream targets
   - Layer assignments are deterministic and stake-weighted
   - Tree structure changes periodically to prevent targeted attacks

4. **Initial Propagation** (Proposer → Layer 1):
   - Proposer distributes different shreds to each Layer 1 validator
   - Each Layer 1 validator receives unique subset of block data
   - Distribution uses UDP for low latency

5. **Cascade Propagation** (Layer N → Layer N+1):
   - Each validator forwards received shreds to assigned downstream nodes
   - Propagation continues until leaves of tree are reached
   - Different paths carry different shreds

6. **Block Reconstruction** (All Nodes):
   - Validators collect shreds from upstream nodes
   - Once minimum threshold of shreds received (data + recovery)
   - Reed-Solomon decoding recovers any missing pieces
   - Original block is reconstructed and verified

This structured approach transforms the bandwidth requirement at each node from O(n) in a flood-based system to O(log n), where n is the number of validators. By leveraging erasure coding and tree-based propagation, GTurbine achieves reliable block distribution while minimizing network congestion and single-node bandwidth requirements.

## Architecture

### Core Components

```
gturbine/
├── gtbuilder/    - Tree construction and management
├── gtencoding/   - Erasure coding and shred serialization 
├── gtnetwork/    - Network transport and routing
├── gtshred/      - Block shredding and reconstruction
└── turbine.go    - Core interfaces and types
```

### Key Features

- **Efficient Erasure Coding**: Uses the battle-tested [klauspost/reedsolomon](https://github.com/klauspost/reedsolomon) library
- **Flexible Tree Structure**: Configurable fanout and layer organization
- **UDP Transport**: Low-latency propagation with erasure coding for reliability
- **Safe Partial Recovery**: Reconstruct blocks with minimum required shreds
- **Built-in Verification**: Integrity checking at both shred and block levels

## Acknowledgments

GTurbine's was made possible by:
- [Solana's Turbine Protocol](https://docs.solana.com/cluster/turbine-block-propagation)
- Academic work on reliable multicast protocols
- The incredible [klauspost/reedsolomon](https://github.com/klauspost/reedsolomon) library

## Support

Found a bug? Have a feature request? Open an issue!

*Remember: In distributed systems, eventual consistency is better than eventual insanity* 😉