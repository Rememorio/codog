package agent

import (
	"errors"
	"strings"
)

type valueOption struct {
	missing            func(string) error
	rejectOutputFormat bool
	set                func(string) error
}

func consumeValueOption(args []string, index *int, options map[string]valueOption) (bool, error) {
	arg := args[*index]
	if option, ok := options[arg]; ok {
		(*index)++
		if *index >= len(args) || option.rejectOutputFormat && isOutputFormatFlag(args[*index]) {
			return true, option.missing(arg)
		}
		return true, option.set(args[*index])
	}
	name, value, inline := strings.Cut(arg, "=")
	option, ok := options[name]
	if !inline || !ok || !strings.HasPrefix(name, "--") {
		return false, nil
	}
	return true, option.set(value)
}

func stringValueOption(target *string, missing string) valueOption {
	return valueOption{missing: func(string) error { return errors.New(missing) }, set: func(value string) error {
		*target = value
		return nil
	}}
}
