package gcrypto

import (
	"bytes"
	"fmt"
	"reflect"
)

// Prefixes are encoded as a fixed width.
const prefixSize = 8

// Registry is a runtime-defined registry to manage encoding and decoding
// a predetermined set of public key types.
type Registry struct {
	byType map[reflect.Type]string

	// For unmarshalling
	byPrefix map[string]NewPubKeyFunc
}

type NewPubKeyFunc func([]byte) (PubKey, error)

func (r *Registry) Register(name string, inst PubKey, newFn NewPubKeyFunc) {
	// TODO: validation on name.
	// TODO: fail if previously registered.

	if len(name) > prefixSize {
		panic(fmt.Errorf(
			"BUG: gcrypto.Registry entries must be <= 8 bytes (got %q, %d bytes)",
			name, len(name),
		))
	}

	if r.byPrefix == nil {
		r.byPrefix = map[string]NewPubKeyFunc{}
	}
	r.byPrefix[name] = newFn

	if r.byType == nil {
		r.byType = map[reflect.Type]string{}
	}
	r.byType[reflect.TypeOf(inst)] = name
}

func (r *Registry) Marshal(pubKey PubKey) []byte {
	var nameHeader [prefixSize]byte

	typ := reflect.TypeOf(pubKey)
	prefix, ok := r.byType[typ]
	if !ok {
		panic(fmt.Errorf(
			"BUG: attempted to Marshal a public key that was never registered (reflect type: %s, type name: %s)",
			typ, pubKey.TypeName(),
		))
	}

	copy(nameHeader[:], prefix)

	return append(nameHeader[:], pubKey.PubKeyBytes()...)
}

// Unmarshal returns a new public key based on b,
// which should be the result of a previous call to [*Registry.Marshal].
//
// Callers should assume that the newly returned PubKey
// will retain a reference to b;
// therefore the slice must not be modified after calling Unmarshal.
func (r *Registry) Unmarshal(b []byte) (PubKey, error) {
	// TODO: more validation against b
	prefix := bytes.TrimRight(b[:prefixSize], "\x00")

	fn := r.byPrefix[string(prefix)]
	if fn == nil {
		return nil, fmt.Errorf("no registered public key type for prefix %q", prefix)
	}

	return fn(b[prefixSize:])
}

// Decode returns a new PubKey from the given type and public key bytes.
// It returns an error if the typeName was not previously registered,
// or if the registered [NewPubKeyFunc] itself returns an error.
//
// Callers must assume that the returned public key retains a reference to b,
// and therefore b must not be modified after calling Decode.
func (r *Registry) Decode(typeName string, b []byte) (PubKey, error) {
	fn := r.byPrefix[typeName]
	if fn == nil {
		return nil, fmt.Errorf("no registered public key type for name %q", typeName)
	}

	return fn(b)
}
