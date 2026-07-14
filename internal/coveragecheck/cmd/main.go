package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Rememorio/codog/internal/coveragecheck"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("coveragecheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	profilePath := flags.String("profile", "", "Go coverage profile")
	diffPath := flags.String("diff", "", "zero-context Git diff")
	modulePath := flags.String("module", "", "Go module path")
	threshold := flags.Float64("threshold", 85, "minimum changed-line coverage percentage")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *profilePath == "" || *diffPath == "" {
		fmt.Fprintln(stderr, "coveragecheck: --profile and --diff are required")
		return 2
	}
	profile, err := os.Open(*profilePath)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = profile.Close() }()
	blocks, err := coveragecheck.ParseProfile(profile, *modulePath)
	if err != nil {
		return fail(stderr, err)
	}
	diff, err := os.Open(*diffPath)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = diff.Close() }()
	changed, err := coveragecheck.ParseDiff(diff)
	if err != nil {
		return fail(stderr, err)
	}
	report := coveragecheck.Evaluate(blocks, changed)
	fmt.Fprintf(stdout, "changed-line coverage: %.2f%% (%d/%d coverable lines)\n", report.Percent, report.Covered, report.Coverable)
	for index, location := range report.Uncovered {
		if index == 20 {
			fmt.Fprintf(stdout, "... and %d more uncovered lines\n", len(report.Uncovered)-index)
			break
		}
		fmt.Fprintf(stdout, "uncovered: %s:%d\n", location.File, location.Line)
	}
	if !report.MeetsThreshold(*threshold) {
		fmt.Fprintf(stderr, "coveragecheck: changed-line coverage %.2f%% is below %.2f%%\n", report.Percent, *threshold)
		return 1
	}
	return 0
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "coveragecheck: %v\n", err)
	return 2
}
