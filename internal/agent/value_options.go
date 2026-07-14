package agent

import (
	"errors"
	"strings"
)

type valueOption struct {
	missing             func(string) error
	rejectEmptySeparate bool
	rejectOutputFormat  bool
	set                 func(string) error
}

func consumeValueOption(args []string, index *int, options map[string]valueOption) (bool, error) {
	arg := args[*index]
	if option, ok := options[arg]; ok {
		(*index)++
		if *index >= len(args) ||
			option.rejectEmptySeparate && strings.TrimSpace(args[*index]) == "" ||
			option.rejectOutputFormat && isOutputFormatFlag(args[*index]) {
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

func parsePreferenceOptions(args []string, command string, usage string, format *string, target *string, path *string) ([]string, error) {
	var rest []string
	missing := func(flag string) error {
		return missingFlagValueError{Command: command, Flag: flag, Usage: usage}
	}
	stringOption := func(destination *string, rejectOutputFormat bool) valueOption {
		return valueOption{missing: missing, rejectOutputFormat: rejectOutputFormat, set: func(value string) error {
			*destination = value
			return nil
		}}
	}
	options := map[string]valueOption{
		"--output-format": stringOption(format, false),
		"-o":              stringOption(format, false),
		"--target":        stringOption(target, true),
		"--path":          stringOption(path, true),
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--json" {
			*format = "json"
			continue
		}
		handled, err := consumeValueOption(args, &index, options)
		if err != nil {
			return rest, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return rest, unknownOptionError{Command: command, Option: arg, Usage: usage}
		}
		rest = append(rest, arg)
	}
	return rest, nil
}
