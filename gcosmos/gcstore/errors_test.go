package gcstore_test

import (
	"errors"
	"testing"

	"github.com/gordian-engine/gordian/gcosmos/gcstore"
	"github.com/stretchr/testify/require"
)

func TestIsAlreadyHaveBlockDataError(t *testing.T) {
	require.True(t, gcstore.IsAlreadyHaveBlockDataError(
		gcstore.AlreadyHaveBlockDataForHeightError{Height: 1},
	))

	require.True(t, gcstore.IsAlreadyHaveBlockDataError(
		gcstore.AlreadyHaveBlockDataForIDError{ID: "foo"},
	))

	require.False(t, gcstore.IsAlreadyHaveBlockDataError(
		errors.New("something else"),
	))

	require.False(t, gcstore.IsAlreadyHaveBlockDataError(nil))
}
