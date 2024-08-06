package tmengine

import "github.com/rollchains/gordian/tm/tmengine/internal/tmemetrics"

// Metrics are the metrics for subsystems within the [Engine].
// The fields in this type should not be considered stable
// and may change without notice between releases.
//
// The type alias is somewhat unfortunate,
// but the alternative would be creating yet another package...
type Metrics = tmemetrics.Metrics
