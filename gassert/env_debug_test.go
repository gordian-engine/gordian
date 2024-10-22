// Only run these tests in debug mode.

//go:build debug

package gassert_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/gordian-engine/gordian/gassert"
	"github.com/stretchr/testify/require"
)

func TestEnvironment_parsing(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   []string
		test func(t *testing.T, e *gassert.Environment)
	}{
		{
			name: "rootWildcard",
			in:   []string{"*"},
			test: func(t *testing.T, e *gassert.Environment) {
				require.True(t, e.Enabled("foo"))
				require.True(t, e.Enabled("foo.bar"))
				require.True(t, e.Enabled("foo.bar.baz"))
				require.True(t, e.Enabled("a"))
			},
		},
		{
			name: "rootedWildcard",
			in:   []string{"foo.*"},
			test: func(t *testing.T, e *gassert.Environment) {
				// Maybe unusual, but we should cover the root not being a match.
				require.False(t, e.Enabled("foo"))

				require.True(t, e.Enabled("foo.bar"))
				require.True(t, e.Enabled("foo.bar.baz"))

				require.False(t, e.Enabled("a"))
			},
		},
		{
			name: "exact",
			in:   []string{"foo.bar", "foo.quux"},
			test: func(t *testing.T, e *gassert.Environment) {
				require.True(t, e.Enabled("foo.bar"))
				require.False(t, e.Enabled("foo.baz"))
				require.True(t, e.Enabled("foo.quux"))
			},
		},
		{
			name: "rootedWildcardWithExclusion",
			in:   []string{"foo.*", "!foo.baz"},
			test: func(t *testing.T, e *gassert.Environment) {
				require.True(t, e.Enabled("foo.bar"))
				require.False(t, e.Enabled("foo.baz"))
				require.True(t, e.Enabled("foo.quux"))
			},
		},
		{
			name: "emptyInput",
			in:   nil,
			test: func(t *testing.T, e *gassert.Environment) {
				require.False(t, e.Enabled("foo.bar"))
			},
		},
	} {
		t.Run("EnvironmentFromString:"+tc.name, func(t *testing.T) {
			t.Parallel()

			e, err := gassert.EnvironmentFromString(strings.Join(tc.in, ","))
			require.NoError(t, err)
			tc.test(t, e)
		})

		t.Run("Parse:"+tc.name, func(t *testing.T) {
			t.Parallel()

			doc := strings.Join(tc.in, "\n")
			e, err := gassert.ParseEnvironment(strings.NewReader(doc))
			require.NoError(t, err)
			tc.test(t, e)
		})
	}
}

func TestEnvironment_parse_errors(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"foo..bar",
		"foo.*.bar",
		"f*o.bar",
	} {
		e, err := gassert.EnvironmentFromString(input)
		require.Error(t, err)
		require.Nil(t, e)

		e, err = gassert.ParseEnvironment(strings.NewReader(input))
		require.Error(t, err)
		require.Nil(t, e)
	}
}

func TestEnvironment_Parse_allowances(t *testing.T) {
	t.Parallel()

	e, err := gassert.ParseEnvironment(strings.NewReader(`# Comment. (Then a blank line.)

foo.bar
baz.*
!baz.quux
`))
	require.NoError(t, err)

	require.True(t, e.Enabled("foo.bar"))
	require.True(t, e.Enabled("baz.foo"))
	require.False(t, e.Enabled("baz.quux"))
}

func TestEnvironment_HandleAssertionFailure_panic(t *testing.T) {
	t.Parallel()

	e, err := gassert.EnvironmentFromString("*")
	require.NoError(t, err)

	require.Panics(t, func() {
		e.HandleAssertionFailure(errors.New("something bad"))
	})

	// Nil panics for a different reason, but we aren't going to get specific in this test.
	require.Panics(t, func() {
		e.HandleAssertionFailure(nil)
	})
}

func TestEnvironment_HandleAssertionFailure_log(t *testing.T) {
	t.Parallel()

	e, err := gassert.EnvironmentFromString("*")
	require.NoError(t, err)

	var buf bytes.Buffer
	e.OnlyLogFailures(slog.New(slog.NewTextHandler(&buf, nil)))

	// When only logging failures, assertion failure does not panic,
	// but the error message is included in a log.
	require.NotPanics(t, func() {
		e.HandleAssertionFailure(errors.New("something bad"))
	})
	require.Contains(t, buf.String(), "something bad")

	// Nil panics even in logging mode.
	require.Panics(t, func() {
		e.HandleAssertionFailure(nil)
	})
}
