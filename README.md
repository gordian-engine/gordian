# Gordian

Gordian is a modular, [Byzantine-fault-tolerant](https://en.wikipedia.org/wiki/Byzantine_fault) consensus engine.

A consensus engine is a [replicated state machine](https://en.wikipedia.org/wiki/State_machine_replication) --
a participant in a distributed system where the participants agree on initial state, actions to apply, and resulting state.

Gordian allows each participant to have a different voting power.
This effectively enables Gordian to be the consensus layer in a [proof of stake](https://en.wikipedia.org/wiki/Proof_of_stake) system.

Because Gordian uses an implementation of [the Tendermint® consensus algorithm](https://arxiv.org/abs/1807.04938)[^1],
systems backed by Gordian can tolerate failure or malicious behavior from participants constituting
up to, but less than, 1/3 of the total voting power.

## Who should use Gordian?

Gordian was primarily written to support blockchain use cases,
but it should fit in any distributed system requiring peers to agree on state.

## How do I use Gordian?

Gordian is a modular system written in [Go](https://go.dev/).

The Gordian core Engine is designed to accept messages from the network
and replay a filtered set of those messages to a state machine.
The state machine consults a user-provided "driver" to track state changes
and determine what data to propose to other network participants.

The core Gordian repository (the one you are looking at now) does **not** provide a Driver.
See [gcosmos](https://github.com/gordian-engine/gcosmos) for an example Driver integrating with the [Cosmos SDK](https://github.com/cosmos/cosmos-sdk).

Outside of the driver, Gordian currently provides a networking layer using [libp2p](https://libp2p.io/),
a storage layer using [SQLite](https://www.sqlite.org/),
Ed25519 signing keys for validators,
and some simple hashing schemes using [BLAKE2b](https://www.blake2.net/).

We have carefully designed the internal APIs to make it as easy as possible to swap out any of those implementations
(and to test that they comply with expectations within the consensus engine).

While it is easiest to integrate Go code with Gordian,
it should be straightforward to write a shim layer in Go such that
you can run an external process and have the core engine communicate with your component.
There is also the option of [CGo](https://pkg.go.dev/cmd/cgo) if you can provide a C ABI.

Refer to the [`_docs` directory](/_docs) for more technical details.

## Why would I use Gordian?

Most other consensus engines tend to have strong opinions about the way these subsystems work.
As a result, when teams run into bottlenecks, they are usually stuck looking for small performance gains
in the underlying framework.
Gordian makes it easy to swap out parts of the consensus engine,
whether you are making one small tweak to a standard component or writing one from scratch.
Whether it's a short experiment or code you've benchmarked and you are taking to production,
you can relax knowing that you can safely maintain one small library,
instead of forking the entire consensus engine.

## Try it out

### Full demo with gcosmos

We recommend the earlier mentioned [gcosmos](https://github.com/gordian-engine/gcosmos) repository
for a full-featured demo involving Gordian integrating with the Cosmos SDK.

### Lightweight demo

Clone the Gordian repository and `cd` into it.
Make sure you have [the most recent version of Go installed](https://go.dev/dl/).
Run `./demo-echo.bash start 4` to start a local network of 4 validators,
running a toy application where the network participants agree on the current height and round.

## License

The Gordian source code is available under the Apache 2.0 license.

Copyright (c) 2024 Strangelove Crypto, Inc.

[^1]: Tendermint is a registered trademark of All in Bits, Inc. and is used here for identification purposes only.
