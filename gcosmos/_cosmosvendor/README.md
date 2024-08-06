# _cosmosvendor

This directory is special for development of the `gcosmos` module during these circumstances:

1. There is not a stable release of the Cosmos SDK with "Server V2" architecture that we need for integrating Gordian and the SDK
2. Developing against the SDK requires use of a go workspace
3. The Gordian core repository has not yet been open sourced, so `gcosmos` is being developed as a nested module with `gordian`

Once all of those points have been addressed, this directory will no longer be used.
