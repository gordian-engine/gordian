// Package gnetdag (Gordian NETwork Directed Acyclic Graph)
// contains types for determining directional flow of network traffic.
//
// Types in this package are focused on int values,
// so that they remain decoupled from any concrete implementations
// of validators, network addresses, and so on.
// Callers may simply use the int values as indices into slices
// of the actual type needing the directed graph.
//
// This package currently contains the [FixedTree] type,
// which effectively maps indices in a slice such that
// every non-root node contains a fixed number of children.
// This package will be expanded with more types as deemed necessary.
package gnetdag
