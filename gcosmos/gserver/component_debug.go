//go:build debug

package gserver

import (
	"github.com/gordian-engine/gordian/gassert"
	"github.com/gordian-engine/gordian/tm/tmengine"
	"github.com/spf13/pflag"
)

const assertRuleFlag = "g-assert-rules"

func addAssertRuleFlag(fs *pflag.FlagSet) {
	// Default to all rules.
	fs.String(assertRuleFlag, "*", "Comma-separated assertion rules. Only available in debug builds. See package docs for github.com/gordian-engine/gordian/gassert.")
}

func getAssertEngineOpt(cfg map[string]any) (tmengine.Opt, error) {
	rules := cfg[assertRuleFlag].(string)
	env, err := gassert.EnvironmentFromString(rules)
	if err != nil {
		return nil, err
	}
	return tmengine.WithAssertEnv(env), nil
}
