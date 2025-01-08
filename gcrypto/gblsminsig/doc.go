// Package gblsminsig wraps [github.com/supranational/blst/bindings/go]
// to provide a [gcrypto.PubKey] implementation backed by BLS keys,
// where the BLS keys have minimized signatures.
//
// We are not currently providing an alternate implementation with minimized keys,
// as signatures are expected to be transmitted and stored much more frequently than keys.
//
// The blst dependency requires CGo,
// so therefore this package also requires CGo.
//
// Two key references for correctly understanding and using BLS keys are
// [RFC9380] (Hashing to Elliptic Curves)
// and the IETF draft for [BLS Signatures].
//
// [RFC9380]: https://www.rfc-editor.org/rfc/rfc9380.html
// [BLS Signatures]: https://datatracker.ietf.org/doc/html/draft-irtf-cfrg-bls-signature-05
package gblsminsig
