package gstress

import (
	"fmt"
	"slices"
	"sync"

	"github.com/rollchains/gordian/tm/tmconsensus"
)

type bState struct {
	startOnce sync.Once
	started   chan struct{}

	mu sync.Mutex

	app        string
	chainID    string
	validators []tmconsensus.Validator
}

func (s *bState) ChainID() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chainID
}

func (s *bState) SetChainID(newID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.chainID = newID
}

func (s *bState) App() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.app
}

func (s *bState) SetApp(a string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.app = a
}

func (s *bState) AddValidator(v tmconsensus.Validator) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if slices.ContainsFunc(s.validators, func(val tmconsensus.Validator) bool {
		return v.PubKey.Equal(val.PubKey)
	}) {
		panic(fmt.Errorf("validator with pub key %q already registered", v.PubKey.PubKeyBytes()))
	}

	s.validators = append(s.validators, v)
}

func (s *bState) Validators() []tmconsensus.Validator {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.validators)
}

func (s *bState) Start() {
	s.startOnce.Do(func() {
		close(s.started)
	})
}
