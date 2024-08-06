package tmconsensustest

import "github.com/rollchains/gordian/tm/tmconsensus"

// AnnotationTestCase is a name and an Annotations value.
// See [AnnotationCombinations] for more details.
type AnnotationTestCase struct {
	Name        string
	Annotations tmconsensus.Annotations
}

// AnnotationCombinations returns a slice of AnnotationTestCase with every combination of
// nil, 0-length slice, and populated User and Driver fields set on the Annotations.
//
// These are useful in tests involving annotations,
// to avoid repetition and to be sure that all cases are covered.
func AnnotationCombinations() []AnnotationTestCase {
	return []AnnotationTestCase{
		{
			Name: "empty annotations",
		},
		{
			Name: "empty but non-nil user annotations",
			Annotations: tmconsensus.Annotations{
				User: []byte{},
			},
		},
		{
			Name: "empty but non-nil driver annotations",
			Annotations: tmconsensus.Annotations{
				Driver: []byte{},
			},
		},
		{
			Name: "empty but non-nil user and driver annotations",
			Annotations: tmconsensus.Annotations{
				User:   []byte{},
				Driver: []byte{},
			},
		},
		{
			Name: "populated user annotations with nil driver annotations",
			Annotations: tmconsensus.Annotations{
				User: []byte("user"),
			},
		},
		{
			Name: "populated user annotations with empty driver annotations",
			Annotations: tmconsensus.Annotations{
				User:   []byte("user"),
				Driver: []byte{},
			},
		},
		{
			Name: "populated driver annotations with nil user annotations",
			Annotations: tmconsensus.Annotations{
				Driver: []byte("driver"),
			},
		},
		{
			Name: "populated driver annotations with empty user annotations",
			Annotations: tmconsensus.Annotations{
				User:   []byte{},
				Driver: []byte("driver"),
			},
		},
		{
			Name: "fully populated",
			Annotations: tmconsensus.Annotations{
				User:   []byte("user"),
				Driver: []byte("driver"),
			},
		},
	}
}
