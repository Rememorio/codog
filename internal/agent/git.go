package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Rememorio/codog/internal/anthropic"
	"github.com/Rememorio/codog/internal/branchlock"
	"github.com/Rememorio/codog/internal/config"
	"github.com/Rememorio/codog/internal/g004conformance"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/greencontract"
	"github.com/Rememorio/codog/internal/releasenotes"
	"github.com/Rememorio/codog/internal/reportconformance"
	"github.com/Rememorio/codog/internal/reportschema"
	"github.com/Rememorio/codog/internal/session"
	"github.com/Rememorio/codog/internal/sessionname"
	"github.com/Rememorio/codog/internal/trustresolver"
)

type branchLockReport struct {
	Kind           string                 `json:"kind"`
	Action         string                 `json:"action"`
	Status         string                 `json:"status"`
	IntentCount    int                    `json:"intent_count"`
	CollisionCount int                    `json:"collision_count"`
	Collisions     []branchlock.Collision `json:"collisions,omitempty"`
}

func (a *App) BranchLock(args []string) error {
	req, err := parseBranchLockArgs(args)
	if err != nil {
		return err
	}
	input, err := a.branchLockInput(req)
	if err != nil {
		return err
	}
	intents, err := branchlock.Decode(input)
	if err != nil {
		return err
	}
	collisions := branchlock.DetectCollisions(intents)
	status := "ok"
	if len(collisions) > 0 {
		status = "collision"
	}
	report := branchLockReport{
		Kind:           "branch_lock",
		Action:         req.Action,
		Status:         status,
		IntentCount:    len(intents),
		CollisionCount: len(collisions),
		Collisions:     collisions,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderBranchLockReport(a.Out, report)
	return nil
}

func (a *App) branchLockInput(req branchLockRequest) ([]byte, error) {
	switch {
	case strings.TrimSpace(req.Input) != "":
		return []byte(req.Input), nil
	case strings.TrimSpace(req.File) != "":
		return os.ReadFile(req.File)
	case req.Stdin:
		in := a.In
		if in == nil {
			in = os.Stdin
		}
		return io.ReadAll(in)
	default:
		return []byte("[]"), nil
	}
}

func parseBranchLockArgs(args []string) (branchLockRequest, error) {
	req := branchLockRequest{Format: "text", Action: "check"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("branch-lock output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--file" || arg == "-f":
			i++
			if i >= len(args) {
				return req, errors.New("branch-lock file is required")
			}
			req.File = args[i]
		case strings.HasPrefix(arg, "--file="):
			req.File = strings.TrimPrefix(arg, "--file=")
		case arg == "--input":
			i++
			if i >= len(args) {
				return req, errors.New("branch-lock input JSON is required")
			}
			req.Input = args[i]
		case strings.HasPrefix(arg, "--input="):
			req.Input = strings.TrimPrefix(arg, "--input=")
		case arg == "--stdin":
			req.Stdin = true
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown branch-lock flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "branch-lock")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	rest := positionals
	action := strings.ToLower(strings.TrimSpace(positionals[0]))
	if action == "check" || action == "detect" || action == "collisions" {
		req.Action = action
		rest = positionals[1:]
	}
	if req.Action == "detect" || req.Action == "collisions" {
		req.Action = "check"
	}
	if req.Action != "check" {
		return req, fmt.Errorf("unknown branch-lock action %q", positionals[0])
	}
	if len(rest) > 1 {
		return req, errors.New("usage: codog branch-lock [check] [FILE|JSON] [--file PATH|--input JSON|--stdin] [--json|--output-format text|json]")
	}
	if len(rest) == 1 {
		value := strings.TrimSpace(rest[0])
		if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") {
			req.Input = value
		} else {
			req.File = value
		}
	}
	if strings.TrimSpace(req.Input) != "" && strings.TrimSpace(req.File) != "" {
		return req, errors.New("branch-lock accepts only one of --input or --file")
	}
	if req.Stdin && (strings.TrimSpace(req.Input) != "" || strings.TrimSpace(req.File) != "") {
		return req, errors.New("branch-lock accepts --stdin only without --input or --file")
	}
	return req, nil
}

func renderBranchLockReport(out io.Writer, report branchLockReport) {
	fmt.Fprintln(out, "Branch Lock")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Intents          %d\n", report.IntentCount)
	fmt.Fprintf(out, "  Collisions       %d\n", report.CollisionCount)
	for _, collision := range report.Collisions {
		fmt.Fprintf(out, "  - branch=%s module=%s lanes=%s\n", collision.Branch, collision.Module, strings.Join(collision.LaneIDs, ", "))
	}
}

type staleBaseRequest struct {
	Format     string
	Action     string
	BaseCommit string
}

type staleBaseReport struct {
	Kind   string                 `json:"kind"`
	Action string                 `json:"action"`
	Status string                 `json:"status"`
	Check  gitops.BaseCommitCheck `json:"check"`
}

func (a *App) StaleBase(args []string) error {
	req, err := parseStaleBaseArgs(args)
	if err != nil {
		return err
	}
	check, err := gitops.CheckBaseCommitForWorkspace(a.Workspace, req.BaseCommit)
	if err != nil {
		return err
	}
	report := staleBaseReport{
		Kind:   "stale_base",
		Action: req.Action,
		Status: check.Status,
		Check:  check,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderStaleBaseReport(a.Out, report)
	return nil
}

func parseStaleBaseArgs(args []string) (staleBaseRequest, error) {
	req := staleBaseRequest{Format: "text", Action: "check"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("stale-base output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--base-commit" || arg == "--base" || arg == "-b":
			i++
			if i >= len(args) {
				return req, errors.New("stale-base base commit is required")
			}
			if err := setStaleBaseCommit(&req, args[i]); err != nil {
				return req, err
			}
		case strings.HasPrefix(arg, "--base-commit="):
			if err := setStaleBaseCommit(&req, strings.TrimPrefix(arg, "--base-commit=")); err != nil {
				return req, err
			}
		case strings.HasPrefix(arg, "--base="):
			if err := setStaleBaseCommit(&req, strings.TrimPrefix(arg, "--base=")); err != nil {
				return req, err
			}
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown stale-base flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "stale-base")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	action := strings.ToLower(strings.TrimSpace(positionals[0]))
	rest := positionals
	if action == "check" || action == "status" {
		req.Action = "check"
		rest = positionals[1:]
	}
	if req.Action != "check" {
		return req, fmt.Errorf("unknown stale-base action %q", positionals[0])
	}
	if len(rest) > 1 {
		return req, errors.New("usage: codog stale-base [check] [BASE_COMMIT] [--base-commit REF] [--json|--output-format text|json]")
	}
	if len(rest) == 1 {
		if strings.TrimSpace(req.BaseCommit) != "" {
			return req, errors.New("stale-base accepts only one base commit")
		}
		if err := setStaleBaseCommit(&req, rest[0]); err != nil {
			return req, err
		}
	}
	return req, nil
}

func setStaleBaseCommit(req *staleBaseRequest, value string) error {
	value = strings.TrimSpace(value)
	if err := gitops.ValidateBaseCommitValue(value); err != nil {
		return err
	}
	req.BaseCommit = value
	return nil
}

func renderStaleBaseReport(out io.Writer, report staleBaseReport) {
	check := report.Check
	fmt.Fprintln(out, "Stale Base")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Status           %s\n", check.Status)
	fmt.Fprintf(out, "  Matches          %t\n", check.Matches)
	if check.Source != nil {
		fmt.Fprintf(out, "  Source           %s\n", check.Source.Kind)
		if check.Source.Path != "" {
			fmt.Fprintf(out, "  Source path      %s\n", check.Source.Path)
		}
	}
	if check.Expected != "" {
		fmt.Fprintf(out, "  Expected         %s\n", check.Expected)
	}
	if check.Actual != "" {
		fmt.Fprintf(out, "  Actual           %s\n", check.Actual)
	}
	if check.Warning != "" {
		fmt.Fprintf(out, "  Warning          %s\n", check.Warning)
	}
}

type greenContractRequest struct {
	Format                         string
	Action                         string
	RequiredLevel                  string
	ObservedLevel                  string
	MergeReady                     bool
	TestCommands                   []greencontract.TestCommandProvenance
	BaseBranchFresh                bool
	RecoveryAttemptContextRecorded bool
	KnownFlakes                    []greencontract.KnownFlake
}

type greenContractReport struct {
	Kind     string                 `json:"kind"`
	Action   string                 `json:"action"`
	Status   string                 `json:"status"`
	Contract greencontract.Contract `json:"contract"`
	Evidence greencontract.Evidence `json:"evidence"`
	Outcome  greencontract.Outcome  `json:"outcome"`
}

func (a *App) GreenContract(args []string) error {
	req, err := parseGreenContractArgs(args)
	if err != nil {
		return err
	}
	var contract greencontract.Contract
	if req.MergeReady {
		contract, err = greencontract.MergeReady(req.RequiredLevel)
	} else {
		contract, err = greencontract.New(req.RequiredLevel)
	}
	if err != nil {
		return err
	}
	evidence := greencontract.Evidence{
		ObservedLevel:                  req.ObservedLevel,
		TestCommands:                   append([]greencontract.TestCommandProvenance(nil), req.TestCommands...),
		BaseBranchFresh:                req.BaseBranchFresh,
		KnownFlakes:                    append([]greencontract.KnownFlake(nil), req.KnownFlakes...),
		RecoveryAttemptContextRecorded: req.RecoveryAttemptContextRecorded,
	}
	outcome, err := contract.EvaluateEvidence(evidence)
	if err != nil {
		return err
	}
	report := greenContractReport{
		Kind:     "green_contract",
		Action:   req.Action,
		Status:   greencontract.StatusForOutcome(outcome),
		Contract: contract,
		Evidence: evidence,
		Outcome:  outcome,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderGreenContractReport(a.Out, report)
	return nil
}

func parseGreenContractArgs(args []string) (greenContractRequest, error) {
	parser := greenContractArgParser{req: greenContractRequest{
		Format:        "text",
		Action:        "check",
		RequiredLevel: greencontract.LevelWorkspace,
		ObservedLevel: greencontract.LevelTargetedTests,
	}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parser.consumeBoolean(arg) {
			continue
		}
		handled, err := consumeValueOption(args, &i, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return parser.req, fmt.Errorf("unknown green-contract flag %q", arg)
		}
		parser.positionals = append(parser.positionals, arg)
	}
	if err := parser.finish(); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

type greenContractArgParser struct {
	req         greenContractRequest
	positionals []string
}

func (p *greenContractArgParser) consumeBoolean(arg string) bool {
	switch arg {
	case "--json":
		p.req.Format = "json"
	case "--merge-ready":
		p.req.MergeReady = true
	case "--base-branch-fresh", "--base-fresh":
		p.req.BaseBranchFresh = true
	case "--recovery-context", "--recovery-attempt-context":
		p.req.RecoveryAttemptContextRecorded = true
	default:
		return false
	}
	return true
}

func (p *greenContractArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format":       p.stringOption(&p.req.Format, "green-contract output format is required"),
		"-o":                    p.stringOption(&p.req.Format, "green-contract output format is required"),
		"--required-level":      p.stringOption(&p.req.RequiredLevel, "green-contract required level is required"),
		"--required":            p.stringOption(&p.req.RequiredLevel, "green-contract required level is required"),
		"--observed-level":      p.stringOption(&p.req.ObservedLevel, "green-contract observed level is required"),
		"--observed":            p.stringOption(&p.req.ObservedLevel, "green-contract observed level is required"),
		"--level":               p.stringOption(&p.req.ObservedLevel, "green-contract observed level is required"),
		"--test-command":        p.testCommandOption(0, "green-contract test command is required"),
		"--failed-test-command": p.testCommandOption(1, "green-contract failed test command is required"),
		"--test-result":         p.testResultOption(),
		"--known-flake":         p.flakeOption(false, "green-contract known flake name is required"),
		"--blocking-flake":      p.flakeOption(true, "green-contract blocking flake name is required"),
	}
}

func (p *greenContractArgParser) stringOption(target *string, message string) valueOption {
	return stringValueOption(target, message)
}

func (p *greenContractArgParser) testCommandOption(exitCode int, message string) valueOption {
	return valueOption{missing: func(string) error { return errors.New(message) }, set: func(value string) error {
		p.req.TestCommands = append(p.req.TestCommands, greencontract.TestCommandProvenance{Command: value, ExitCode: exitCode})
		return nil
	}}
}

func (p *greenContractArgParser) testResultOption() valueOption {
	return valueOption{missing: func(string) error { return errors.New("green-contract test result is required") }, set: func(value string) error {
		testCommand, err := parseGreenTestResult(value)
		if err != nil {
			return err
		}
		p.req.TestCommands = append(p.req.TestCommands, testCommand)
		return nil
	}}
}

func (p *greenContractArgParser) flakeOption(blocking bool, message string) valueOption {
	return valueOption{missing: func(string) error { return errors.New(message) }, set: func(value string) error {
		p.req.KnownFlakes = append(p.req.KnownFlakes, greencontract.KnownFlake{TestName: value, BlocksGreen: blocking})
		return nil
	}}
}

func (p *greenContractArgParser) finish() error {
	normalized, err := normalizeTextOrJSON(p.req.Format, "green-contract")
	if err != nil {
		return err
	}
	p.req.Format = normalized
	if len(p.positionals) > 0 {
		action := strings.ToLower(strings.TrimSpace(p.positionals[0]))
		if action == "check" || action == "status" || action == "verify" {
			p.req.Action = "check"
			p.positionals = p.positionals[1:]
		}
	}
	if len(p.positionals) > 0 {
		return errors.New("usage: codog green-contract [check] [--merge-ready] [--required-level LEVEL] [--observed-level LEVEL] [--test-command COMMAND] [--test-result COMMAND=EXIT] [--base-branch-fresh] [--recovery-context] [--blocking-flake NAME] [--json|--output-format text|json]")
	}
	level, err := greencontract.NormalizeLevel(p.req.RequiredLevel)
	if err != nil {
		return err
	}
	p.req.RequiredLevel = level
	level, err = greencontract.NormalizeLevel(p.req.ObservedLevel)
	if err != nil {
		return err
	}
	p.req.ObservedLevel = level
	return nil
}

func parseGreenTestResult(value string) (greencontract.TestCommandProvenance, error) {
	parts := strings.LastIndex(value, "=")
	if parts <= 0 || parts == len(value)-1 {
		return greencontract.TestCommandProvenance{}, errors.New("green-contract test result must use COMMAND=EXIT")
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(value[parts+1:]))
	if err != nil {
		return greencontract.TestCommandProvenance{}, fmt.Errorf("green-contract test result exit code: %w", err)
	}
	command := strings.TrimSpace(value[:parts])
	if command == "" {
		return greencontract.TestCommandProvenance{}, errors.New("green-contract test result command is required")
	}
	return greencontract.TestCommandProvenance{Command: command, ExitCode: exitCode}, nil
}

func renderGreenContractReport(out io.Writer, report greenContractReport) {
	fmt.Fprintln(out, "Green Contract")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Required level   %s\n", report.Contract.RequiredLevel)
	fmt.Fprintf(out, "  Observed level   %s\n", report.Evidence.ObservedLevel)
	if len(report.Contract.Requirements) > 0 {
		fmt.Fprintf(out, "  Requirements     %s\n", strings.Join(report.Contract.Requirements, ", "))
	}
	if len(report.Evidence.TestCommands) > 0 {
		fmt.Fprintln(out, "  Test commands")
		for _, command := range report.Evidence.TestCommands {
			fmt.Fprintf(out, "    - exit=%d %s\n", command.ExitCode, command.Command)
		}
	}
	fmt.Fprintf(out, "  Base fresh       %t\n", report.Evidence.BaseBranchFresh)
	fmt.Fprintf(out, "  Recovery context %t\n", report.Evidence.RecoveryAttemptContextRecorded)
	if len(report.Outcome.Missing) > 0 {
		fmt.Fprintf(out, "  Missing          %s\n", strings.Join(report.Outcome.Missing, ", "))
	}
	if len(report.Outcome.BlockingFlakes) > 0 {
		fmt.Fprintln(out, "  Blocking flakes")
		for _, flake := range report.Outcome.BlockingFlakes {
			fmt.Fprintf(out, "    - %s\n", flake.TestName)
		}
	}
}

type g004ConformanceRequest struct {
	Format string
	Action string
	Input  string
	File   string
	Stdin  bool
}

type g004ConformanceReport struct {
	Kind       string                  `json:"kind"`
	Action     string                  `json:"action"`
	Status     string                  `json:"status"`
	Schema     string                  `json:"schema"`
	Valid      bool                    `json:"valid"`
	ErrorCount int                     `json:"error_count"`
	Errors     []g004conformance.Error `json:"errors,omitempty"`
}

func (a *App) G004Conformance(args []string) error {
	req, err := parseG004ConformanceArgs(args)
	if err != nil {
		return err
	}
	data, err := readG004ConformanceInput(req, a.In)
	if err != nil {
		return err
	}
	result, err := g004conformance.ValidateJSON(data)
	if err != nil {
		return err
	}
	status := "invalid"
	if result.Valid {
		status = "ok"
	}
	report := g004ConformanceReport{
		Kind:       "g004_conformance",
		Action:     req.Action,
		Status:     status,
		Schema:     result.Schema,
		Valid:      result.Valid,
		ErrorCount: result.ErrorCount,
		Errors:     result.Errors,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderG004ConformanceReport(a.Out, report)
	return nil
}

func parseG004ConformanceArgs(args []string) (g004ConformanceRequest, error) {
	req := g004ConformanceRequest{Format: "text", Action: "validate"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("g004-conformance output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--input":
			i++
			if i >= len(args) {
				return req, errors.New("g004-conformance input JSON is required")
			}
			req.Input = args[i]
		case strings.HasPrefix(arg, "--input="):
			req.Input = strings.TrimPrefix(arg, "--input=")
		case arg == "--file" || arg == "-f":
			i++
			if i >= len(args) {
				return req, errors.New("g004-conformance file is required")
			}
			req.File = args[i]
		case strings.HasPrefix(arg, "--file="):
			req.File = strings.TrimPrefix(arg, "--file=")
		case arg == "--stdin":
			req.Stdin = true
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown g004-conformance flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "g004-conformance")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) > 0 {
		action := strings.ToLower(strings.TrimSpace(positionals[0]))
		if action == "validate" || action == "check" || action == "verify" {
			req.Action = "validate"
			positionals = positionals[1:]
		}
	}
	if len(positionals) > 1 {
		return req, errors.New("usage: codog g004-conformance [validate] [FILE|JSON] [--input JSON|--file PATH|--stdin] [--output-format text|json]")
	}
	if len(positionals) == 1 {
		value := strings.TrimSpace(positionals[0])
		if strings.HasPrefix(value, "{") {
			req.Input = value
		} else {
			req.File = value
		}
	}
	if strings.TrimSpace(req.Input) != "" && strings.TrimSpace(req.File) != "" {
		return req, errors.New("g004-conformance accepts only one of --input or --file")
	}
	if req.Stdin && (strings.TrimSpace(req.Input) != "" || strings.TrimSpace(req.File) != "") {
		return req, errors.New("g004-conformance accepts --stdin only without --input or --file")
	}
	if strings.TrimSpace(req.Input) == "" && strings.TrimSpace(req.File) == "" && !req.Stdin {
		return req, errors.New("g004-conformance input is required")
	}
	return req, nil
}

func readG004ConformanceInput(req g004ConformanceRequest, stdin io.Reader) ([]byte, error) {
	switch {
	case strings.TrimSpace(req.Input) != "":
		return []byte(req.Input), nil
	case strings.TrimSpace(req.File) != "":
		return os.ReadFile(req.File)
	case req.Stdin:
		if stdin == nil {
			return nil, errors.New("g004-conformance stdin is not available")
		}
		return io.ReadAll(stdin)
	default:
		return nil, errors.New("g004-conformance input is required")
	}
}

func renderG004ConformanceReport(out io.Writer, report g004ConformanceReport) {
	fmt.Fprintln(out, "G004 Conformance")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Schema           %s\n", report.Schema)
	fmt.Fprintf(out, "  Valid            %t\n", report.Valid)
	fmt.Fprintf(out, "  Errors           %d\n", report.ErrorCount)
	for _, err := range report.Errors {
		fmt.Fprintf(out, "  - %s: %s\n", err.Path, err.Message)
	}
}

type reportSchemaRequest struct {
	Format         string
	Action         string
	Input          string
	File           string
	Stdin          bool
	View           string
	Consumer       string
	ReportIDs      []string
	SchemaVersions []string
	SchemaFilter   bool
	FieldFamilies  []string
	MaxSensitivity string
}

type reportSchemaReport struct {
	Kind             string                           `json:"kind"`
	Action           string                           `json:"action"`
	Status           string                           `json:"status"`
	Registry         *reportschema.Registry           `json:"registry,omitempty"`
	Report           *reportschema.CanonicalReport    `json:"report,omitempty"`
	Projection       *reportschema.Projection         `json:"projection,omitempty"`
	Conformance      *reportconformance.Result        `json:"conformance,omitempty"`
	ConformanceCases []reportconformance.RequiredCase `json:"conformance_cases,omitempty"`
}

func (a *App) ReportSchema(args []string) error {
	req, err := parseReportSchemaArgs(args)
	if err != nil {
		return err
	}
	out := reportSchemaReport{Kind: "report_schema", Action: req.Action, Status: "ok"}
	switch req.Action {
	case "registry":
		registry := reportschema.RegistryV1()
		registry = reportschema.FilterRegistry(registry, reportschema.RegistryFilter{
			ReportIDs:      append([]string(nil), req.ReportIDs...),
			SchemaVersions: schemaFilterValues(req),
			FieldFamilies:  append([]string(nil), req.FieldFamilies...),
		})
		out.Registry = &registry
	case "canonicalize":
		report, err := readReportSchemaInput(req, a.In)
		if err != nil {
			return err
		}
		canonical, err := reportschema.Canonicalize(report)
		if err != nil {
			return err
		}
		out.Report = &canonical
	case "project":
		report, err := readReportSchemaInput(req, a.In)
		if err != nil {
			return err
		}
		projection, err := reportschema.Project(report, reportschema.ConsumerCapabilities{
			Consumer:       req.Consumer,
			SchemaVersions: append([]string(nil), req.SchemaVersions...),
			FieldFamilies:  append([]string(nil), req.FieldFamilies...),
			MaxSensitivity: req.MaxSensitivity,
		}, req.View)
		if err != nil {
			return err
		}
		out.Projection = &projection
	case "conformance":
		data, err := readReportSchemaRawInput(req, a.In)
		if err != nil {
			return err
		}
		result, err := reportconformance.ValidateJSON(data)
		if err != nil {
			return err
		}
		if !result.Valid {
			out.Status = "invalid"
		}
		out.Conformance = &result
	case "conformance-fixtures":
		out.ConformanceCases = reportconformance.RequiredCases()
	default:
		return fmt.Errorf("unknown report-schema action %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderReportSchemaReport(a.Out, out)
	return nil
}

func parseReportSchemaArgs(args []string) (reportSchemaRequest, error) {
	parser := reportSchemaArgParser{req: reportSchemaRequest{
		Format:         "text",
		Action:         "registry",
		View:           "default",
		Consumer:       "codog",
		SchemaVersions: []string{reportschema.SchemaV1},
		MaxSensitivity: reportschema.SensitivityPublic,
	}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if parser.consumeBoolean(arg) {
			continue
		}
		handled, err := consumeValueOption(args, &i, parser.valueOptions())
		if err != nil {
			return parser.req, err
		}
		if handled {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return parser.req, fmt.Errorf("unknown report-schema flag %q", arg)
		}
		parser.positionals = append(parser.positionals, arg)
	}
	if err := parser.finish(); err != nil {
		return parser.req, err
	}
	return parser.req, nil
}

type reportSchemaArgParser struct {
	req         reportSchemaRequest
	positionals []string
}

func (p *reportSchemaArgParser) consumeBoolean(arg string) bool {
	switch arg {
	case "--json":
		p.req.Format = "json"
	case "--stdin":
		p.req.Stdin = true
	default:
		return false
	}
	return true
}

func (p *reportSchemaArgParser) valueOptions() map[string]valueOption {
	return map[string]valueOption{
		"--output-format":  stringValueOption(&p.req.Format, "report-schema output format is required"),
		"-o":               stringValueOption(&p.req.Format, "report-schema output format is required"),
		"--input":          stringValueOption(&p.req.Input, "report-schema input JSON is required"),
		"--file":           stringValueOption(&p.req.File, "report-schema file is required"),
		"-f":               stringValueOption(&p.req.File, "report-schema file is required"),
		"--view":           stringValueOption(&p.req.View, "report-schema view is required"),
		"--consumer":       stringValueOption(&p.req.Consumer, "report-schema consumer is required"),
		"--report":         p.appendOption(&p.req.ReportIDs, "report-schema report id is required"),
		"--schema-version": p.schemaVersionOption(),
		"--field-family":   p.appendOption(&p.req.FieldFamilies, "report-schema field family is required"),
		"--max-sensitivity": stringValueOption(
			&p.req.MaxSensitivity, "report-schema max sensitivity is required",
		),
	}
}

func (p *reportSchemaArgParser) appendOption(target *[]string, message string) valueOption {
	return valueOption{missing: func(string) error { return errors.New(message) }, set: func(value string) error {
		*target = append(*target, value)
		return nil
	}}
}

func (p *reportSchemaArgParser) schemaVersionOption() valueOption {
	return valueOption{missing: func(string) error { return errors.New("report-schema schema version is required") }, set: func(value string) error {
		p.req.SchemaVersions = appendSchemaVersion(p.req.SchemaVersions, value)
		p.req.SchemaFilter = true
		return nil
	}}
}

func (p *reportSchemaArgParser) finish() error {
	normalized, err := normalizeTextOrJSON(p.req.Format, "report-schema")
	if err != nil {
		return err
	}
	p.req.Format = normalized
	if len(p.positionals) > 0 {
		action := strings.ToLower(strings.TrimSpace(p.positionals[0]))
		switch action {
		case "registry", "schema", "fields":
			p.req.Action = "registry"
		case "canonicalize", "canonicalise", "canonical":
			p.req.Action = "canonicalize"
		case "project", "projection":
			p.req.Action = "project"
		case "conformance", "consumer-conformance", "validate-consumer":
			p.req.Action = "conformance"
		case "conformance-fixtures", "fixtures", "consumer-fixtures":
			p.req.Action = "conformance-fixtures"
		default:
			return fmt.Errorf("unknown report-schema action %q", p.positionals[0])
		}
		p.positionals = p.positionals[1:]
	}
	if len(p.positionals) > 0 {
		return errors.New("usage: codog report-schema [registry|canonicalize|project|conformance|conformance-fixtures] [--input JSON|--file PATH|--stdin] [--report ID] [--schema-version VERSION] [--consumer NAME] [--field-family NAME] [--max-sensitivity public|internal|operator_only|secret] [--output-format text|json]")
	}
	if strings.TrimSpace(p.req.Input) != "" && strings.TrimSpace(p.req.File) != "" {
		return errors.New("report-schema accepts only one of --input or --file")
	}
	if p.req.Stdin && (strings.TrimSpace(p.req.Input) != "" || strings.TrimSpace(p.req.File) != "") {
		return errors.New("report-schema accepts --stdin only without --input or --file")
	}
	if (p.req.Action == "canonicalize" || p.req.Action == "project" || p.req.Action == "conformance") && strings.TrimSpace(p.req.Input) == "" && strings.TrimSpace(p.req.File) == "" && !p.req.Stdin {
		return errors.New("report-schema input is required for canonicalize, project, and conformance")
	}
	return nil
}

func appendSchemaVersion(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	if len(values) == 1 && values[0] == reportschema.SchemaV1 {
		return []string{value}
	}
	return append(values, value)
}

func schemaFilterValues(req reportSchemaRequest) []string {
	if !req.SchemaFilter {
		return nil
	}
	return append([]string(nil), req.SchemaVersions...)
}

func readReportSchemaRawInput(req reportSchemaRequest, stdin io.Reader) ([]byte, error) {
	switch {
	case strings.TrimSpace(req.Input) != "":
		return []byte(req.Input), nil
	case strings.TrimSpace(req.File) != "":
		return os.ReadFile(req.File)
	case req.Stdin:
		if stdin == nil {
			return nil, errors.New("report-schema stdin is not available")
		}
		return io.ReadAll(stdin)
	default:
		return nil, errors.New("report-schema input is required")
	}
}

func readReportSchemaInput(req reportSchemaRequest, stdin io.Reader) (reportschema.CanonicalReport, error) {
	var data []byte
	var err error
	switch {
	case strings.TrimSpace(req.Input) != "":
		data = []byte(req.Input)
	case strings.TrimSpace(req.File) != "":
		data, err = os.ReadFile(req.File)
		if err != nil {
			return reportschema.CanonicalReport{}, err
		}
	case req.Stdin:
		if stdin == nil {
			return reportschema.CanonicalReport{}, errors.New("report-schema stdin is not available")
		}
		data, err = io.ReadAll(stdin)
		if err != nil {
			return reportschema.CanonicalReport{}, err
		}
	default:
		return reportschema.CanonicalReport{}, errors.New("report-schema input is required")
	}
	var report reportschema.CanonicalReport
	if err := json.Unmarshal(data, &report); err != nil {
		return reportschema.CanonicalReport{}, fmt.Errorf("report-schema input JSON: %w", err)
	}
	return report, nil
}

func renderReportSchemaReport(out io.Writer, report reportSchemaReport) {
	fmt.Fprintln(out, "Report Schema")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	if report.Registry != nil {
		fmt.Fprintf(out, "  Schema           %s\n", report.Registry.SchemaVersion)
		fmt.Fprintf(out, "  Fields           %d\n", len(report.Registry.Fields))
		for _, field := range report.Registry.Fields {
			required := "optional"
			if field.Required {
				required = "required"
			}
			extra := ""
			if len(field.EnumValues) > 0 {
				extra = " enum=" + strings.Join(field.EnumValues, "|")
			}
			if field.Deprecated {
				extra += " deprecated"
			}
			fmt.Fprintf(out, "    - %s [%s] %s%s\n", field.ID, required, field.FieldFamily, extra)
		}
		if len(report.Registry.Reports) > 0 {
			fmt.Fprintf(out, "  Reports          %d\n", len(report.Registry.Reports))
			for _, candidate := range report.Registry.Reports {
				fmt.Fprintf(out, "    - %s %s\n", candidate.ID, candidate.SchemaVersion)
			}
		}
	}
	if report.Report != nil {
		fmt.Fprintf(out, "  Schema           %s\n", report.Report.SchemaVersion)
		fmt.Fprintf(out, "  Report ID        %s\n", report.Report.Identity.ReportID)
		fmt.Fprintf(out, "  Content hash     %s\n", report.Report.Identity.ContentHash)
		fmt.Fprintf(out, "  Claims           %d\n", len(report.Report.Claims))
	}
	if report.Projection != nil {
		fmt.Fprintf(out, "  Schema           %s\n", report.Projection.SchemaVersion)
		fmt.Fprintf(out, "  Projection ID    %s\n", report.Projection.ProjectionID)
		fmt.Fprintf(out, "  View             %s\n", report.Projection.View)
		fmt.Fprintf(out, "  Consumer         %s\n", report.Projection.Provenance.Consumer)
		fmt.Fprintf(out, "  Downgraded       %t\n", report.Projection.Provenance.Downgraded)
		if len(report.Projection.Provenance.OmittedFieldFamilies) > 0 {
			fmt.Fprintf(out, "  Omitted          %s\n", strings.Join(report.Projection.Provenance.OmittedFieldFamilies, ", "))
		}
		if len(report.Projection.Provenance.Redactions) > 0 {
			fmt.Fprintln(out, "  Redactions")
			for _, redaction := range report.Projection.Provenance.Redactions {
				fmt.Fprintf(out, "    - %s %s\n", redaction.FieldPath, redaction.Reason)
			}
		}
	}
	if report.Conformance != nil {
		fmt.Fprintf(out, "  Schema           %s\n", report.Conformance.SchemaVersion)
		fmt.Fprintf(out, "  Fixture set      %s\n", report.Conformance.FixtureSet)
		fmt.Fprintf(out, "  Consumer         %s %s\n", report.Conformance.Consumer.Name, report.Conformance.Consumer.Version)
		fmt.Fprintf(out, "  Valid            %t\n", report.Conformance.Valid)
		fmt.Fprintf(out, "  Parse passed     %t\n", report.Conformance.ParsePassed)
		fmt.Fprintf(out, "  Semantic passed  %t\n", report.Conformance.SemanticPassed)
		fmt.Fprintf(out, "  Cases            %d/%d\n", report.Conformance.PassedCaseCount, report.Conformance.RequiredCaseCount)
		if report.Conformance.LastPassed != nil {
			fmt.Fprintf(out, "  Last passed      %s %s %s\n", report.Conformance.LastPassed.Consumer, report.Conformance.LastPassed.Version, report.Conformance.LastPassed.PassedAt)
		}
		for _, err := range report.Conformance.Errors {
			fmt.Fprintf(out, "  - %s [%s] %s\n", err.Path, err.Kind, err.Message)
		}
	}
	if len(report.ConformanceCases) > 0 {
		fmt.Fprintf(out, "  Fixture set      %s\n", reportconformance.FixtureSetVersion)
		fmt.Fprintf(out, "  Conformance cases %d\n", len(report.ConformanceCases))
		for _, candidate := range report.ConformanceCases {
			fmt.Fprintf(out, "    - %s %s %s\n", candidate.Name, candidate.View, candidate.ProjectionID)
		}
	}
}

type trustRequest struct {
	Format   string
	Action   string
	CWD      string
	Worktree string
	Screen   string
	Allow    []string
	Deny     []string
	NoEvents bool
}

type trustReport struct {
	Kind     string                 `json:"kind"`
	Action   string                 `json:"action"`
	Status   string                 `json:"status"`
	CWD      string                 `json:"cwd"`
	Worktree string                 `json:"worktree,omitempty"`
	Decision trustresolver.Decision `json:"decision"`
}

func (a *App) Trust(args []string) error {
	req, err := parseTrustArgs(args, a.Workspace)
	if err != nil {
		return err
	}
	cfg := trustresolver.Config{
		Allowlisted: trustAllowlistEntries(req.Allow),
		Denied:      append([]string(nil), req.Deny...),
		EmitEvents:  true,
	}
	resolver := trustresolver.New(cfg)
	if req.NoEvents {
		resolver = trustresolver.NewWithoutEvents(cfg)
	}
	decision := resolver.Resolve(req.CWD, req.Worktree, req.Screen)
	report := trustReport{
		Kind:     "trust",
		Action:   req.Action,
		Status:   decision.Status,
		CWD:      req.CWD,
		Worktree: req.Worktree,
		Decision: decision,
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTrustReport(a.Out, report)
	return nil
}

func parseTrustArgs(args []string, defaultCWD string) (trustRequest, error) {
	req := trustRequest{Format: "text", Action: "resolve", CWD: strings.TrimSpace(defaultCWD)}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("trust output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--cwd":
			i++
			if i >= len(args) {
				return req, errors.New("trust cwd is required")
			}
			req.CWD = args[i]
		case strings.HasPrefix(arg, "--cwd="):
			req.CWD = strings.TrimPrefix(arg, "--cwd=")
		case arg == "--worktree":
			i++
			if i >= len(args) {
				return req, errors.New("trust worktree is required")
			}
			req.Worktree = args[i]
		case strings.HasPrefix(arg, "--worktree="):
			req.Worktree = strings.TrimPrefix(arg, "--worktree=")
		case arg == "--screen":
			i++
			if i >= len(args) {
				return req, errors.New("trust screen text is required")
			}
			req.Screen = args[i]
		case strings.HasPrefix(arg, "--screen="):
			req.Screen = strings.TrimPrefix(arg, "--screen=")
		case arg == "--allow":
			i++
			if i >= len(args) {
				return req, errors.New("trust allow pattern is required")
			}
			req.Allow = append(req.Allow, args[i])
		case strings.HasPrefix(arg, "--allow="):
			req.Allow = append(req.Allow, strings.TrimPrefix(arg, "--allow="))
		case arg == "--deny":
			i++
			if i >= len(args) {
				return req, errors.New("trust deny root is required")
			}
			req.Deny = append(req.Deny, args[i])
		case strings.HasPrefix(arg, "--deny="):
			req.Deny = append(req.Deny, strings.TrimPrefix(arg, "--deny="))
		case arg == "--no-events":
			req.NoEvents = true
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown trust flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "trust")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) > 0 {
		action := strings.ToLower(strings.TrimSpace(positionals[0]))
		if action == "resolve" || action == "check" || action == "status" {
			req.Action = "resolve"
			positionals = positionals[1:]
		}
	}
	if strings.TrimSpace(req.Screen) == "" && len(positionals) > 0 {
		req.Screen = strings.Join(positionals, " ")
	}
	if strings.TrimSpace(req.CWD) == "" {
		return req, errors.New("trust cwd is required")
	}
	return req, nil
}

func trustAllowlistEntries(patterns []string) []trustresolver.AllowlistEntry {
	entries := make([]trustresolver.AllowlistEntry, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		entries = append(entries, trustresolver.AllowlistEntry{Pattern: pattern})
	}
	return entries
}

func renderTrustReport(out io.Writer, report trustReport) {
	decision := report.Decision
	fmt.Fprintln(out, "Trust")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	fmt.Fprintf(out, "  Status           %s\n", decision.Status)
	fmt.Fprintf(out, "  CWD              %s\n", report.CWD)
	if report.Worktree != "" {
		fmt.Fprintf(out, "  Worktree         %s\n", report.Worktree)
	}
	fmt.Fprintf(out, "  Prompt detected  %t\n", decision.PromptDetected)
	fmt.Fprintf(out, "  Trusted          %t\n", decision.Trusted)
	if decision.Policy != "" {
		fmt.Fprintf(out, "  Policy           %s\n", decision.Policy)
	}
	if decision.Resolution != "" {
		fmt.Fprintf(out, "  Resolution       %s\n", decision.Resolution)
	}
	if decision.MatchedPattern != "" {
		fmt.Fprintf(out, "  Matched          %s\n", decision.MatchedPattern)
	}
	if len(decision.Events) > 0 {
		fmt.Fprintln(out, "  Events")
		for _, event := range decision.Events {
			fmt.Fprintf(out, "    - %s", event.Type)
			if event.Policy != "" {
				fmt.Fprintf(out, " policy=%s", event.Policy)
			}
			if event.Resolution != "" {
				fmt.Fprintf(out, " resolution=%s", event.Resolution)
			}
			if event.Reason != "" {
				fmt.Fprintf(out, " reason=%s", event.Reason)
			}
			fmt.Fprintln(out)
		}
	}
}

type sessionTagRequest struct {
	Format    string
	SessionID string
	Tag       string
	Confirm   bool
	Help      bool
}

type sessionTagReport struct {
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Status      string `json:"status"`
	SessionID   string `json:"session_id"`
	Tag         string `json:"tag,omitempty"`
	PreviousTag string `json:"previous_tag,omitempty"`
	Message     string `json:"message"`
}

// SessionTag toggles a searchable tag on a resumed conversation. Git tags
// remain available through the direct `codog tag` command.
func (a *App) SessionTag(args []string, overrides config.FlagOverrides) error {
	return a.sessionTagCommand(args, overrides, nil)
}

func (a *App) sessionTagCommand(args []string, overrides config.FlagOverrides, current *session.Session) error {
	req, err := parseSessionTagArgs(args, overrides)
	if err != nil {
		return err
	}
	if req.Help {
		if req.Format == "json" {
			report := sessionTagReport{Kind: "session_tag", Action: "help", Status: "ok", SessionID: req.SessionID, Message: sessionTagHelp}
			data, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(a.Out, string(data))
			return nil
		}
		fmt.Fprintln(a.Out, sessionTagHelp)
		return nil
	}
	if a.Sessions == nil {
		return errors.New("session store is not configured")
	}
	if current == nil || (req.SessionID != "latest" && current.ID != req.SessionID) {
		current, err = a.Sessions.OpenExisting(req.SessionID)
		if err != nil {
			return err
		}
	}
	tag := session.NormalizeSessionTag(req.Tag)
	if tag == "" {
		return errors.New("tag name cannot be empty")
	}
	previous := session.NormalizeSessionTag(current.Identity.Tag)
	report := sessionTagReport{
		Kind:        "session_tag",
		Action:      "set",
		Status:      "ok",
		SessionID:   current.ID,
		Tag:         tag,
		PreviousTag: previous,
		Message:     "Tagged session with #" + tag,
	}
	if previous == tag {
		report.Action = "remove"
		if !req.Confirm {
			report.Status = "confirmation_required"
			report.Message = "Tag #" + tag + " is already set. Run the command again with --confirm to remove it."
			return renderSessionTagReport(a.Out, req.Format, report)
		}
		identity, setErr := a.Sessions.SetTag(current.ID, "")
		if setErr != nil {
			return setErr
		}
		current.Identity.Tag = identity.Tag
		report.Tag = ""
		report.Message = "Removed tag #" + tag
		return renderSessionTagReport(a.Out, req.Format, report)
	}
	identity, err := a.Sessions.SetTag(current.ID, tag)
	if err != nil {
		return err
	}
	current.Identity.Tag = identity.Tag
	return renderSessionTagReport(a.Out, req.Format, report)
}

func parseSessionTagArgs(args []string, overrides config.FlagOverrides) (sessionTagRequest, error) {
	const usage = "/tag <tag-name> [--confirm] [--json|--output-format text|json]"
	req := sessionTagRequest{Format: "text", SessionID: "latest"}
	if strings.TrimSpace(overrides.Resume) != "" {
		req.SessionID = strings.TrimSpace(overrides.Resume)
	} else if strings.TrimSpace(overrides.SessionID) != "" {
		req.SessionID = strings.TrimSpace(overrides.SessionID)
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "tag", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "tag", Flag: arg, Usage: usage}
			}
			req.SessionID = strings.TrimSpace(args[index])
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimSpace(strings.TrimPrefix(arg, "--session="))
		case arg == "--confirm":
			req.Confirm = true
		case arg == "--help" || arg == "-h":
			req.Help = true
		case arg == "--":
			positionals = append(positionals, args[index+1:]...)
			index = len(args)
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "tag", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	format, err := normalizeOutputFormat("tag", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = format
	if len(positionals) == 1 && strings.EqualFold(strings.TrimSpace(positionals[0]), "help") {
		req.Help = true
		positionals = nil
	}
	req.Tag = strings.Join(positionals, " ")
	if strings.TrimSpace(req.Tag) == "" {
		req.Help = true
	}
	return req, nil
}

func renderSessionTagReport(out io.Writer, format string, report sessionTagReport) error {
	if format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(out, string(data))
		return nil
	}
	fmt.Fprintln(out, report.Message)
	return nil
}

type tagRequest struct {
	Format  string
	Action  string
	Name    string
	Ref     string
	Message string
	Pattern string
	Limit   int
}

type tagReport struct {
	Kind    string           `json:"kind"`
	Action  string           `json:"action"`
	Status  string           `json:"status"`
	Pattern string           `json:"pattern,omitempty"`
	Tags    []gitops.TagInfo `json:"tags,omitempty"`
	Output  string           `json:"output,omitempty"`
}

func (a *App) Tag(args []string) error {
	req, err := parseTagArgs(args)
	if err != nil {
		return err
	}
	report := tagReport{Kind: "tag", Action: req.Action, Status: "ok", Pattern: req.Pattern}
	switch req.Action {
	case "list":
		tags, err := gitops.ListTags(a.Workspace, req.Pattern, req.Limit)
		if err != nil {
			return err
		}
		report.Tags = tags
	case "create":
		output, err := gitops.CreateTag(a.Workspace, req.Name, req.Ref, req.Message)
		if err != nil {
			return err
		}
		report.Output = output
		report.Tags, err = gitops.ListTags(a.Workspace, req.Name, 1)
		if err != nil {
			return err
		}
	case "show":
		output, err := gitops.ShowTag(a.Workspace, req.Name)
		if err != nil {
			return err
		}
		report.Output = output
	case "delete":
		output, err := gitops.DeleteTag(a.Workspace, req.Name)
		if err != nil {
			return err
		}
		report.Output = output
	default:
		return fmt.Errorf("unknown tag action %q", req.Action)
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderTagReport(a.Out, report)
	return nil
}

func parseTagArgs(args []string) (tagRequest, error) {
	const usage = "codog tag [list [PATTERN]|create NAME [REF]|show NAME|delete NAME] [--limit N] [--message TEXT] [--json|--output-format text|json]"
	req := tagRequest{Format: "text", Action: "list", Limit: 50}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, missingFlagValueError{Command: "tag", Flag: arg, Usage: usage}
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--limit":
			i++
			if i >= len(args) {
				return req, missingFlagValueError{Command: "tag", Flag: arg, Usage: usage}
			}
			limit, err := strconv.Atoi(args[i])
			if err != nil || limit < 0 {
				return req, invalidFlagValueError{Flag: "--limit", Value: args[i], Message: "tag limit must be a non-negative integer", Usage: usage}
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			value := strings.TrimPrefix(arg, "--limit=")
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 0 {
				return req, invalidFlagValueError{Flag: "--limit", Value: value, Message: "tag limit must be a non-negative integer", Usage: usage}
			}
			req.Limit = limit
		case arg == "--message" || arg == "-m":
			i++
			if i >= len(args) {
				return req, missingFlagValueError{Command: "tag", Flag: arg, Usage: usage}
			}
			req.Message = args[i]
		case strings.HasPrefix(arg, "--message="):
			req.Message = strings.TrimPrefix(arg, "--message=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "tag", Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	normalizedFormat, err := normalizeOutputFormat("tag", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if len(positionals) == 0 {
		return req, nil
	}
	req.Action = strings.ToLower(positionals[0])
	rest := positionals[1:]
	switch req.Action {
	case "list", "ls":
		req.Action = "list"
		if len(rest) > 0 {
			req.Pattern = rest[0]
		}
	case "create", "add":
		req.Action = "create"
		if len(rest) == 0 {
			return req, requiredArgumentError{Command: "tag create", Argument: "NAME", Usage: usage}
		}
		req.Name = rest[0]
		if len(rest) > 1 {
			req.Ref = rest[1]
		}
	case "show":
		if len(rest) == 0 {
			return req, requiredArgumentError{Command: "tag show", Argument: "NAME", Usage: usage}
		}
		req.Name = rest[0]
	case "delete", "del", "remove", "rm":
		req.Action = "delete"
		if len(rest) == 0 {
			return req, requiredArgumentError{Command: "tag delete", Argument: "NAME", Usage: usage}
		}
		req.Name = rest[0]
	default:
		return req, unexpectedExtraArgsError{Command: "tag", Args: []string{positionals[0]}, Usage: usage}
	}
	return req, nil
}

func renderTagReport(out io.Writer, report tagReport) {
	fmt.Fprintln(out, "Tags")
	fmt.Fprintf(out, "  Action           %s\n", report.Action)
	if report.Pattern != "" {
		fmt.Fprintf(out, "  Pattern          %s\n", report.Pattern)
	}
	if strings.TrimSpace(report.Output) != "" {
		fmt.Fprintf(out, "  Output           %s\n", strings.ReplaceAll(strings.TrimSpace(report.Output), "\n", "\n                   "))
	}
	if len(report.Tags) == 0 {
		return
	}
	fmt.Fprintf(out, "  Count            %d\n", len(report.Tags))
	fmt.Fprintln(out)
	for _, tag := range report.Tags {
		detail := tag.Commit
		if tag.Subject != "" {
			detail = strings.TrimSpace(detail + " " + tag.Subject)
		}
		fmt.Fprintf(out, "  %s", tag.Name)
		if detail != "" {
			fmt.Fprintf(out, "  %s", detail)
		}
		fmt.Fprintln(out)
	}
}

func (a *App) Changelog(args []string) error {
	req, err := parseChangelogArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Changelog(a.Workspace, req.Limit)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		entries, err := gitops.LogEntries(a.Workspace, req.Limit)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(changelogReport{
			Kind:    "changelog",
			Action:  "show",
			Status:  "ok",
			Limit:   req.Limit,
			Count:   len(entries),
			Entries: entries,
			Raw:     raw,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

type changelogRequest struct {
	Format string
	Limit  int
}

type changelogReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Limit   int               `json:"limit"`
	Count   int               `json:"count"`
	Entries []gitops.LogEntry `json:"entries"`
	Raw     string            `json:"raw"`
}

func parseChangelogArgs(args []string) (changelogRequest, error) {
	req := changelogRequest{Format: "text", Limit: 10}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("changelog output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown changelog flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "changelog")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	if len(positionals) > 1 {
		return req, errors.New("usage: codog changelog [count] [--json|--output-format text|json]")
	}
	limit, err := strconv.Atoi(positionals[0])
	if err != nil || limit <= 0 {
		return req, errors.New("changelog count must be a positive integer")
	}
	req.Limit = limit
	return req, nil
}

type releaseNotesRequest struct {
	Format string
	From   string
	To     string
	Limit  int
}

func (a *App) ReleaseNotes(args []string) error {
	req, err := parseReleaseNotesArgs(args)
	if err != nil {
		return err
	}
	report, err := releasenotes.Generate(a.Workspace, releasenotes.Options{
		From:  req.From,
		To:    req.To,
		Limit: req.Limit,
	})
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	releasenotes.RenderMarkdown(a.Out, report)
	return nil
}

func parseReleaseNotesArgs(args []string) (releaseNotesRequest, error) {
	req := releaseNotesRequest{Format: "markdown", Limit: 50}
	var positional []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--format" || arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--from":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes from ref is required")
			}
			req.From = args[index]
		case strings.HasPrefix(arg, "--from="):
			req.From = strings.TrimPrefix(arg, "--from=")
		case arg == "--to":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes to ref is required")
			}
			req.To = args[index]
		case strings.HasPrefix(arg, "--to="):
			req.To = strings.TrimPrefix(arg, "--to=")
		case arg == "--limit":
			index++
			if index >= len(args) {
				return releaseNotesRequest{}, errors.New("release-notes limit is required")
			}
			limit, err := strconv.Atoi(args[index])
			if err != nil {
				return releaseNotesRequest{}, err
			}
			req.Limit = limit
		case strings.HasPrefix(arg, "--limit="):
			limit, err := strconv.Atoi(strings.TrimPrefix(arg, "--limit="))
			if err != nil {
				return releaseNotesRequest{}, err
			}
			req.Limit = limit
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) > 2 {
		return releaseNotesRequest{}, errors.New("usage: codog release-notes [FROM [TO]] [--from REF] [--to REF] [--limit N] [--format markdown|json]")
	}
	if len(positional) > 0 && req.From == "" {
		req.From = positional[0]
	}
	if len(positional) > 1 && req.To == "" {
		req.To = positional[1]
	}
	switch req.Format {
	case "markdown", "text":
		req.Format = "markdown"
	case "json":
	default:
		return releaseNotesRequest{}, fmt.Errorf("unknown release-notes format %q", req.Format)
	}
	return req, nil
}

func (a *App) Stash(args []string) error {
	req, err := parseStashArgs(args)
	if err != nil {
		return err
	}
	var output string
	switch req.Action {
	case "list", "show":
		output, err = gitops.StashList(a.Workspace)
	case "push", "save":
		output, err = gitops.StashPush(a.Workspace, req.Push)
	case "apply":
		output, err = gitops.StashApply(a.Workspace, req.Ref)
	case "pop":
		output, err = gitops.StashPop(a.Workspace, req.Ref)
	default:
		return fmt.Errorf("unknown stash action %q", req.Action)
	}
	if err != nil {
		return err
	}
	if output == "" {
		output = "No output."
	}
	if req.Format == "json" {
		stashes, err := gitops.ListStashes(a.Workspace)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(stashReport{
			Kind:    "stash",
			Action:  req.Action,
			Status:  "ok",
			Ref:     req.Ref,
			Output:  output,
			Count:   len(stashes),
			Stashes: stashes,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, output)
	return nil
}

type stashRequest struct {
	Format string
	Action string
	Ref    string
	Push   gitops.StashPushOptions
}

type stashReport struct {
	Kind    string             `json:"kind"`
	Action  string             `json:"action"`
	Status  string             `json:"status"`
	Ref     string             `json:"ref,omitempty"`
	Output  string             `json:"output"`
	Count   int                `json:"count"`
	Stashes []gitops.StashInfo `json:"stashes"`
}

func parseStashArgs(args []string) (stashRequest, error) {
	req := stashRequest{Format: "text", Action: "list"}
	var positionals []string
	var pushArgs []string
	collectPushArgs := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("stash output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case collectPushArgs:
			pushArgs = append(pushArgs, arg)
		default:
			positionals = append(positionals, arg)
			if len(positionals) == 1 {
				action := strings.ToLower(strings.TrimSpace(positionals[0]))
				collectPushArgs = action == "push" || action == "save"
			}
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "stash")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 {
		return req, nil
	}
	req.Action = strings.ToLower(strings.TrimSpace(positionals[0]))
	switch req.Action {
	case "list", "show":
		if len(positionals) > 1 || len(pushArgs) > 0 {
			return req, errors.New("usage: codog stash list [--json|--output-format text|json]")
		}
	case "push", "save":
		req.Push = parseStashPushArgs(pushArgs)
	case "apply", "pop":
		rest := positionals[1:]
		if len(pushArgs) > 0 {
			rest = append(rest, pushArgs...)
		}
		if len(rest) > 1 {
			return req, fmt.Errorf("usage: codog stash %s [stash-ref] [--json|--output-format text|json]", req.Action)
		}
		if len(rest) == 1 {
			req.Ref = rest[0]
		}
	default:
		return req, fmt.Errorf("unknown stash action %q", req.Action)
	}
	return req, nil
}

func parseStashPushArgs(args []string) gitops.StashPushOptions {
	options := gitops.StashPushOptions{}
	message := []string{}
	for _, arg := range args {
		switch arg {
		case "--include-untracked", "-u":
			options.IncludeUntracked = true
		default:
			message = append(message, arg)
		}
	}
	options.Message = strings.Join(message, " ")
	return options
}

type gitBlameRequest struct {
	Format string
	Path   string
	Line   int
}

type gitBlameReport struct {
	Kind    string              `json:"kind"`
	Action  string              `json:"action"`
	Status  string              `json:"status"`
	Path    string              `json:"path"`
	Line    int                 `json:"line,omitempty"`
	Count   int                 `json:"count"`
	Entries []gitops.BlameEntry `json:"entries"`
	Raw     string              `json:"raw"`
}

func (a *App) GitBlame(args []string) error {
	req, err := parseGitBlameArgs(args)
	if err != nil {
		return err
	}
	raw, err := gitops.Blame(a.Workspace, req.Path, req.Line)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		entries, err := gitops.BlameEntries(a.Workspace, req.Path, req.Line)
		if err != nil {
			return err
		}
		data, _ := json.MarshalIndent(gitBlameReport{
			Kind:    "git_blame",
			Action:  "show",
			Status:  "ok",
			Path:    req.Path,
			Line:    req.Line,
			Count:   len(entries),
			Entries: entries,
			Raw:     raw,
		}, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintln(a.Out, raw)
	return nil
}

func parseGitBlameArgs(args []string) (gitBlameRequest, error) {
	req := gitBlameRequest{Format: "text"}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git blame output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown git blame flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	normalized, err := normalizeTextOrJSON(req.Format, "git blame")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	if len(positionals) == 0 || len(positionals) > 2 {
		return req, errors.New("usage: codog git blame FILE [line] [--json|--output-format text|json]")
	}
	req.Path = positionals[0]
	if len(positionals) == 2 {
		parsed, err := strconv.Atoi(positionals[1])
		if err != nil || parsed <= 0 {
			return req, errors.New("blame line must be a positive integer")
		}
		req.Line = parsed
	}
	return req, nil
}

func (a *App) handleDiffSlash(args []string) {
	req, err := parseDiffArgs(args)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	report, err := a.buildDiffReport(req)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	if report.Empty {
		fmt.Fprintln(a.Out, "No diff.")
		return
	}
	fmt.Fprintln(a.Out, report.Diff)
}

func (a *App) handleLogSlash(args []string) {
	if err := a.GitLog(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleChangelogSlash(args []string) {
	if err := a.Changelog(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleBlameSlash(args []string) {
	if err := a.GitBlame(args); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) handleCommitSlash(args []string) {
	req, err := parseGitCommitArgs(args, "text")
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	report, err := a.buildCommitReport(req)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return
	}
	fmt.Fprintf(a.Err, "commit %s\n", report.Commit)
	if report.Summary != "" {
		fmt.Fprintln(a.Out, report.Summary)
	}
}

type gitCommitRequest struct {
	Format  string
	Options gitops.CommitOptions
}

type commitReport struct {
	Kind    string `json:"kind"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	All     bool   `json:"all"`
	Commit  string `json:"commit"`
	Summary string `json:"summary"`
	Output  string `json:"output,omitempty"`
}

func (a *App) GitCommit(args []string, defaultFormat string) error {
	req, err := parseGitCommitArgs(args, defaultFormat)
	if err != nil {
		return err
	}
	report, err := a.buildCommitReport(req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderCommitReport(a.Out, report)
	return nil
}

func (a *App) buildCommitReport(req gitCommitRequest) (commitReport, error) {
	result, err := gitops.Commit(a.Workspace, req.Options)
	if err != nil {
		return commitReport{}, err
	}
	return commitReport{
		Kind:    "commit",
		Action:  "create",
		Status:  "ok",
		All:     req.Options.All,
		Commit:  result.Commit,
		Summary: result.Summary,
		Output:  result.Output,
	}, nil
}

func renderCommitReport(out io.Writer, report commitReport) {
	fmt.Fprintln(out, "Commit")
	fmt.Fprintf(out, "  Status           %s\n", report.Status)
	fmt.Fprintf(out, "  Commit           %s\n", report.Commit)
	fmt.Fprintf(out, "  All              %t\n", report.All)
	if strings.TrimSpace(report.Summary) != "" {
		fmt.Fprintf(out, "  Summary          %s\n", report.Summary)
	}
	if strings.TrimSpace(report.Output) != "" {
		fmt.Fprintf(out, "  Output           %s\n", strings.ReplaceAll(strings.TrimSpace(report.Output), "\n", "\n                   "))
	}
}

func parseGitCommitArgs(args []string, defaultFormat string) (gitCommitRequest, error) {
	req := gitCommitRequest{Format: defaultFormat}
	normalized, err := normalizeTextOrJSON(req.Format, "git commit")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	message := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--all" || arg == "-a":
			req.Options.All = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			i++
			if i >= len(args) {
				return req, errors.New("git commit output format is required")
			}
			req.Format = args[i]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--":
			message = append(message, args[i+1:]...)
			i = len(args)
		default:
			message = append(message, arg)
		}
	}
	normalized, err = normalizeTextOrJSON(req.Format, "git commit")
	if err != nil {
		return req, err
	}
	req.Format = normalized
	req.Options.Message = strings.Join(message, " ")
	if strings.TrimSpace(req.Options.Message) == "" {
		return req, requiredArgumentError{
			Command:  "commit",
			Argument: "commit message",
			Usage:    "codog commit [--all] MESSAGE [--json|--output-format text|json]",
		}
	}
	return req, nil
}

type exportRequest struct {
	SessionID string
	Output    string
	Format    string
}

type renameRequest struct {
	SessionID string
	NewID     string
	Format    string
}

type generateSessionNameRequest struct {
	SessionID string
	Source    string
	Format    string
	Prefix    string
	Text      string
	MaxWords  int
	Rename    bool
}

type shareRequest struct {
	SessionID string
	OutputDir string
	Format    string
	JSON      bool
}

type shareReport struct {
	Kind         string               `json:"kind"`
	Action       string               `json:"action"`
	Status       string               `json:"status"`
	SessionID    string               `json:"session_id"`
	File         string               `json:"file"`
	Format       string               `json:"format"`
	Messages     int                  `json:"messages"`
	Bytes        int                  `json:"bytes"`
	GitStateFile string               `json:"git_state_file,omitempty"`
	GitState     *draftGitStateReport `json:"git_state,omitempty"`
}

type copyRequest struct {
	SessionID string
	Scope     string
	Nth       int
	Format    string
	JSON      bool
}

type copyReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	SessionID string `json:"session_id"`
	Scope     string `json:"scope"`
	Nth       int    `json:"nth,omitempty"`
	Format    string `json:"format"`
	Bytes     int    `json:"bytes"`
	Clipboard string `json:"clipboard"`
}

type pasteRequest struct {
	SessionID string
	Format    string
	JSON      bool
	Print     bool
	MaxBytes  int
}

type pasteReport struct {
	Kind      string `json:"kind"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	Bytes     int    `json:"bytes"`
	Lines     int    `json:"lines"`
	Clipboard string `json:"clipboard"`
	Submitted bool   `json:"submitted"`
	Preview   string `json:"preview,omitempty"`
}

type clipboardImage struct {
	Data      []byte
	MediaType string
	Extension string
	Clipboard string
}

type pinRequest struct {
	SessionID    string
	Format       string
	MessageIndex int
}

type pinReport struct {
	Kind           string `json:"kind"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	SessionID      string `json:"session_id"`
	Path           string `json:"path"`
	MessageIndex   int    `json:"message_index"`
	DisplayIndex   int    `json:"display_index"`
	MessageCount   int    `json:"message_count"`
	PinnedMessages []int  `json:"pinned_messages"`
}

var writeClipboard = writeSystemClipboard
var readClipboard = readSystemClipboard
var readClipboardImage = readSystemClipboardImage

var errNoClipboardImage = errors.New("clipboard does not contain an image")

func (a *App) GenerateSessionName(args []string, overrides config.FlagOverrides) error {
	report, format, err := a.generateSessionNameReport(args, overrides)
	if err != nil {
		return err
	}
	if format == "json" {
		return sessionname.RenderJSON(a.Out, report)
	}
	sessionname.RenderText(a.Out, report)
	return nil
}

func (a *App) generateSessionNameReport(args []string, overrides config.FlagOverrides) (sessionname.Report, string, error) {
	req, err := parseGenerateSessionNameArgs(args, overrides)
	if err != nil {
		return sessionname.Report{}, "", err
	}
	if strings.TrimSpace(req.Text) == "" && a.Sessions == nil {
		return sessionname.Report{}, "", errors.New("session store is not configured")
	}
	sourceText := req.Text
	source := "text"
	sessionID := req.SessionID
	messageCount := 0
	path := ""
	if strings.TrimSpace(sourceText) == "" {
		sess, err := a.Sessions.Open(req.SessionID)
		if err != nil {
			return sessionname.Report{}, "", err
		}
		sessionID = sess.ID
		messageCount = len(sess.Messages)
		path = sess.Path
		sourceText, source, err = a.generateSessionNameSource(sess, req.Source)
		if err != nil {
			return sessionname.Report{}, "", err
		}
	}
	base := sessionname.Suggest(sourceText, sessionname.Options{Prefix: req.Prefix, MaxWords: req.MaxWords})
	suggested := base
	collisions := 0
	if a.Sessions != nil {
		suggested, collisions, err = sessionname.Unique(base, func(id string) (bool, error) {
			if id == sessionID {
				return false, nil
			}
			return a.Sessions.Exists(id)
		})
		if err != nil {
			return sessionname.Report{}, "", err
		}
	}
	report := sessionname.Report{
		Kind:           "session_name",
		Action:         "generate",
		Status:         "ok",
		SessionID:      sessionID,
		SuggestedID:    suggested,
		Source:         source,
		SourceText:     truncateForReport(sourceText, 240),
		MessageCount:   messageCount,
		CollisionCount: collisions,
		Path:           path,
	}
	if !req.Rename {
		return report, req.Format, nil
	}
	if a.Sessions == nil {
		return sessionname.Report{}, "", errors.New("session store is not configured")
	}
	if strings.TrimSpace(sessionID) == "" {
		return sessionname.Report{}, "", errors.New("session id is required for rename")
	}
	report.Action = "rename"
	if suggested == sessionID {
		report.Status = "unchanged"
		report.Messages = append(report.Messages, "Generated name already matches the session id.")
		return report, req.Format, nil
	}
	renamed, err := a.Sessions.Rename(sessionID, suggested)
	if err != nil {
		return sessionname.Report{}, "", err
	}
	report.Status = "renamed"
	report.Renamed = true
	report.OldID = renamed.OldID
	report.NewID = renamed.NewID
	report.Path = renamed.NewPath
	report.MessageCount = renamed.MessageCount
	return report, req.Format, nil
}

func (a *App) generateSessionNameSource(sess *session.Session, source string) (string, string, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "first"
	}
	history, err := a.Sessions.PromptHistory(sess.ID)
	if err != nil {
		return "", "", err
	}
	if len(history) > 0 {
		switch source {
		case "first", "oldest":
			return history[0].Text, "first_prompt", nil
		case "last", "latest", "recent":
			return history[len(history)-1].Text, "last_prompt", nil
		default:
			return "", "", fmt.Errorf("unknown generateSessionName source %q", source)
		}
	}
	if strings.TrimSpace(sess.ID) != "" {
		return sess.ID, "session_id", nil
	}
	return "session", "fallback", nil
}

func truncateForReport(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func parseGenerateSessionNameArgs(args []string, overrides config.FlagOverrides) (generateSessionNameRequest, error) {
	req := generateSessionNameRequest{SessionID: "latest", Source: "first", Format: "text", MaxWords: 7}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, errors.New("generateSessionName output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("generateSessionName session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, errors.New("generateSessionName resume id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--source":
			index++
			if index >= len(args) {
				return req, errors.New("generateSessionName source is required")
			}
			req.Source = args[index]
		case strings.HasPrefix(arg, "--source="):
			req.Source = strings.TrimPrefix(arg, "--source=")
		case arg == "--prefix":
			index++
			if index >= len(args) {
				return req, errors.New("generateSessionName prefix is required")
			}
			req.Prefix = args[index]
		case strings.HasPrefix(arg, "--prefix="):
			req.Prefix = strings.TrimPrefix(arg, "--prefix=")
		case arg == "--max-words":
			index++
			if index >= len(args) {
				return req, errors.New("generateSessionName max words is required")
			}
			value, err := parsePositiveInt(args[index], "generateSessionName max words")
			if err != nil {
				return req, err
			}
			req.MaxWords = value
		case strings.HasPrefix(arg, "--max-words="):
			value, err := parsePositiveInt(strings.TrimPrefix(arg, "--max-words="), "generateSessionName max words")
			if err != nil {
				return req, err
			}
			req.MaxWords = value
		case arg == "--text":
			index++
			if index >= len(args) {
				return req, errors.New("generateSessionName text is required")
			}
			req.Text = args[index]
		case strings.HasPrefix(arg, "--text="):
			req.Text = strings.TrimPrefix(arg, "--text=")
		case arg == "--rename" || arg == "--apply":
			req.Rename = true
		default:
			return req, fmt.Errorf("unknown generateSessionName argument %q", arg)
		}
	}
	if err := validateTextOrJSON(req.Format, "generateSessionName"); err != nil {
		return req, err
	}
	source := strings.ToLower(strings.TrimSpace(req.Source))
	switch source {
	case "first", "oldest", "last", "latest", "recent":
		req.Source = source
	default:
		return req, fmt.Errorf("unknown generateSessionName source %q", req.Source)
	}
	return req, nil
}

func (a *App) Rename(args []string, overrides config.FlagOverrides) error {
	req, err := parseRenameArgs(args, overrides)
	if err != nil {
		return err
	}
	result, err := a.Sessions.Rename(req.SessionID, req.NewID)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	fmt.Fprintf(a.Out, "Renamed session %s to %s (%d messages).\n", result.OldID, result.NewID, result.MessageCount)
	return nil
}

func parseRenameArgs(args []string, overrides config.FlagOverrides) (renameRequest, error) {
	req := renameRequest{SessionID: "latest", Format: "text"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format":
			index++
			if index >= len(args) {
				return req, errors.New("rename output format is required")
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, errors.New("rename session id is required")
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case strings.HasPrefix(arg, "-"):
			return req, fmt.Errorf("unknown rename flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) != 1 {
		return req, errors.New("usage: codog rename NEW_ID [--session ID] [--json]")
	}
	req.NewID = positionals[0]
	switch req.Format {
	case "text", "json":
		return req, nil
	default:
		return req, fmt.Errorf("unknown rename output format %q", req.Format)
	}
}

func (a *App) Export(args []string) error {
	return a.ExportWithOverrides(args, config.FlagOverrides{})
}

func (a *App) ExportWithOverrides(args []string, overrides config.FlagOverrides) error {
	defaultSession := "latest"
	if strings.TrimSpace(overrides.Resume) != "" {
		defaultSession = overrides.Resume
	} else if strings.TrimSpace(overrides.SessionID) != "" {
		defaultSession = overrides.SessionID
	}
	req, err := parseExportArgs(args, defaultSession)
	if err != nil {
		return err
	}
	return a.writeExport(req)
}

func (a *App) writeExport(req exportRequest) error {
	data, sess, err := a.Sessions.Export(req.SessionID, req.Format)
	if err != nil {
		return err
	}
	if req.Output == "" {
		_, err = a.Out.Write(data)
		return err
	}
	path, err := a.resolveWorkspaceOutputPath(req.Output)
	if err != nil {
		return exportFilesystemError{Operation: "resolve_output_path", Path: req.Output, Err: err}
	}
	if err := session.ValidateExportOutputPath(path); err != nil {
		return exportFilesystemError{Operation: "validate_output_path", Path: path, Err: err}
	}
	path, err = writeUniqueExportFile(path, data)
	if err != nil {
		return exportFilesystemError{Operation: "write_output", Path: path, Err: err}
	}
	format, _ := session.NormalizeExportFormat(req.Format)
	report := map[string]any{
		"session_id": sess.ID,
		"file":       path,
		"format":     format,
		"messages":   len(sess.Messages),
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Fprintln(a.Out, string(encoded))
	return nil
}

func (a *App) Share(args []string, overrides config.FlagOverrides) error {
	req, err := parseShareArgs(args, overrides)
	if err != nil {
		return err
	}
	data, sess, err := a.Sessions.Export(req.SessionID, req.Format)
	if err != nil {
		return err
	}
	format, _ := session.NormalizeExportFormat(req.Format)
	outputDir := req.OutputDir
	if outputDir == "" {
		outputDir = filepath.Join(a.Workspace, ".codog", "share")
	} else {
		outputDir, err = a.resolveWorkspaceOutputPath(outputDir)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(outputDir, shareFileName(sess.ID, format))
	if err := session.ValidateExportOutputPath(path); err != nil {
		return err
	}
	gitStateFile := ""
	var gitStateSummary *draftGitStateReport
	var gitStateData []byte
	if state, err := gitops.PreserveStateForIssue(a.Workspace); err != nil {
		return err
	} else if state != nil {
		gitStateFile = filepath.Join(outputDir, shareGitStateFileName(sess.ID))
		stateData, err := json.MarshalIndent(state, "", "  ")
		if err != nil {
			return err
		}
		if err := session.ValidateExportOutputPath(gitStateFile); err != nil {
			return err
		}
		gitStateData = stateData
		gitStateSummary = draftGitStateSummary(state)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if gitStateFile != "" {
		if err := os.WriteFile(gitStateFile, gitStateData, 0o644); err != nil {
			return err
		}
	}
	report := shareReport{
		Kind:         "share",
		Action:       "create",
		Status:       "ok",
		SessionID:    sess.ID,
		File:         path,
		Format:       format,
		Messages:     len(sess.Messages),
		Bytes:        len(data),
		GitStateFile: gitStateFile,
		GitState:     gitStateSummary,
	}
	if req.JSON {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	fmt.Fprintf(a.Out, "Shared session %s to %s (%d bytes).\n", report.SessionID, report.File, report.Bytes)
	if report.GitStateFile != "" {
		fmt.Fprintf(a.Out, "Git state saved to %s.\n", report.GitStateFile)
	}
	return nil
}

func parseShareArgs(args []string, overrides config.FlagOverrides) (shareRequest, error) {
	const usage = "codog share [OUTPUT_DIR] [--session ID] [--format markdown|json|jsonl|html] [--json]"
	req := shareRequest{SessionID: "latest", Format: session.ExportMarkdown}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.JSON = true
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "share", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--format":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "share", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case arg == "--output" || arg == "--output-dir":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "share", Flag: arg, Usage: usage}
			}
			req.OutputDir = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.OutputDir = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "--output-dir="):
			req.OutputDir = strings.TrimPrefix(arg, "--output-dir=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "share", Option: arg, Usage: usage}
		default:
			if req.OutputDir != "" {
				return req, unexpectedExtraArgsError{Command: "share", Args: []string{arg}, Usage: usage}
			}
			req.OutputDir = arg
		}
	}
	if _, err := session.NormalizeExportFormat(req.Format); err != nil {
		return req, err
	}
	return req, nil
}

func shareFileName(sessionID string, format string) string {
	ext := "md"
	switch format {
	case session.ExportJSON:
		ext = "json"
	case session.ExportJSONL:
		ext = "jsonl"
	case session.ExportHTML:
		ext = "html"
	}
	return shareSafeSessionID(sessionID) + "." + ext
}

func shareGitStateFileName(sessionID string) string {
	return shareSafeSessionID(sessionID) + ".git-state.json"
}

func shareSafeSessionID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "session"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(sessionID) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if ok {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(builder.String(), "-.")
	if out == "" {
		return "session"
	}
	return out
}

func (a *App) Copy(ctx context.Context, args []string, overrides config.FlagOverrides) error {
	req, err := parseCopyArgs(args, overrides)
	if err != nil {
		return err
	}
	data, sess, format, err := a.copyPayload(req)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return errors.New("nothing to copy")
	}
	clipboard, err := writeClipboard(ctx, data)
	if err != nil {
		return err
	}
	nth := 0
	if req.Scope == "nth" {
		nth = req.Nth
	}
	report := copyReport{
		Kind:      "copy",
		Action:    "copy",
		Status:    "ok",
		SessionID: sess.ID,
		Scope:     req.Scope,
		Nth:       nth,
		Format:    format,
		Bytes:     len(data),
		Clipboard: clipboard,
	}
	if req.JSON {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	fmt.Fprintf(a.Out, "Copied %s from session %s to clipboard (%d bytes).\n", copyScopeLabel(req), sess.ID, len(data))
	return nil
}

func (a *App) Paste(ctx context.Context, args []string, overrides config.FlagOverrides) error {
	req, err := parsePasteArgs(args, overrides)
	if err != nil {
		return err
	}
	data, clipboard, err := pasteClipboardPayload(ctx, req)
	if err != nil {
		return err
	}
	if req.JSON {
		report := buildPasteReport(req, data, clipboard, false)
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return nil
	}
	_, err = a.Out.Write(data)
	return err
}

func (a *App) handlePasteSlash(ctx context.Context, args []string, sess *session.Session) {
	req, err := parsePasteArgs(args, config.FlagOverrides{SessionID: sess.ID})
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	data, clipboard, err := pasteClipboardPayload(ctx, req)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.JSON {
		report := buildPasteReport(req, data, clipboard, false)
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(encoded))
		return
	}
	if req.Print {
		if _, err := a.Out.Write(data); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
		return
	}
	if err := a.runSessionTurnWithOptions(ctx, "repl", sess, string(data), "idle", turnOptions{}); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
	}
}

func (a *App) Pin(args []string, overrides config.FlagOverrides) error {
	return a.setPin("pin", args, overrides)
}

func (a *App) Unpin(args []string, overrides config.FlagOverrides) error {
	return a.setPin("unpin", args, overrides)
}

func (a *App) setPin(action string, args []string, overrides config.FlagOverrides) error {
	req, err := parsePinArgs(action, args, overrides)
	if err != nil {
		return err
	}
	return a.runPinRequest(action, req)
}

func (a *App) runPinRequest(action string, req pinRequest) error {
	if req.MessageIndex < 0 {
		sess, err := a.Sessions.OpenExisting(req.SessionID)
		if err != nil {
			return err
		}
		if len(sess.Messages) == 0 {
			return errors.New("session has no messages")
		}
		req.SessionID = sess.ID
		req.MessageIndex = len(sess.Messages) - 1
	}
	var result session.PinResult
	var err error
	if action == "unpin" {
		result, err = a.Sessions.UnpinMessage(req.SessionID, req.MessageIndex)
	} else {
		result, err = a.Sessions.PinMessage(req.SessionID, req.MessageIndex)
	}
	if err != nil {
		return err
	}
	report := pinReport{
		Kind:           "message_pin",
		Action:         result.Action,
		Status:         "ok",
		SessionID:      result.SessionID,
		Path:           result.Path,
		MessageIndex:   result.MessageIndex,
		DisplayIndex:   result.MessageIndex + 1,
		MessageCount:   result.MessageCount,
		PinnedMessages: append([]int(nil), result.PinnedMessages...),
	}
	if req.Format == "json" {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(a.Out, string(data))
		return nil
	}
	renderPinReport(a.Out, report)
	return nil
}

func parseSessionPinArgs(command string, args []string, defaultSession string, defaultFormat string) (pinRequest, error) {
	usage := command + " SESSION [message-index|last] [--json|--output-format text|json]"
	req := pinRequest{SessionID: strings.TrimSpace(defaultSession), Format: defaultFormat, MessageIndex: -1}
	if req.Format == "" {
		req.Format = "text"
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session" || arg == "--resume":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: command, Option: arg, Usage: usage}
		default:
			positionals = append(positionals, arg)
		}
	}
	if strings.TrimSpace(defaultSession) == "" {
		if len(positionals) == 0 && strings.TrimSpace(req.SessionID) == "" {
			return req, fmt.Errorf("usage: %s", usage)
		}
		if len(positionals) > 0 && strings.TrimSpace(req.SessionID) == "" {
			req.SessionID = positionals[0]
			positionals = positionals[1:]
		}
	}
	if len(positionals) > 1 {
		return req, unexpectedExtraArgsError{Command: command, Args: append([]string(nil), positionals[1:]...), Usage: usage}
	}
	if len(positionals) == 1 {
		index, err := parsePinMessageIndex(positionals[0], command)
		if err != nil {
			return req, err
		}
		req.MessageIndex = index
	}
	normalizedFormat, err := normalizeOutputFormat(command, req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func parsePinArgs(command string, args []string, overrides config.FlagOverrides) (pinRequest, error) {
	usage := "codog " + command + " [message-index|last] [--session ID] [--json|--output-format text|json]"
	req := pinRequest{SessionID: "latest", Format: "text", MessageIndex: -1}
	if strings.TrimSpace(overrides.Resume) != "" {
		req.SessionID = overrides.Resume
	}
	if strings.TrimSpace(overrides.SessionID) != "" {
		req.SessionID = overrides.SessionID
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: command, Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{
				Command: command,
				Option:  arg,
				Usage:   usage,
			}
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) > 1 {
		return req, unexpectedExtraArgsError{
			Command: command,
			Args:    append([]string(nil), positionals[1:]...),
			Usage:   usage,
		}
	}
	if len(positionals) == 1 {
		index, err := parsePinMessageIndex(positionals[0], command)
		if err != nil {
			return req, err
		}
		req.MessageIndex = index
	}
	normalizedFormat, err := normalizeOutputFormat(command, req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	return req, nil
}

func parsePinMessageIndex(value string, command string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "last") || strings.EqualFold(value, "latest") {
		return -1, nil
	}
	display, err := strconv.Atoi(value)
	if err != nil || display < 1 {
		return 0, fmt.Errorf("%s message index must be a positive integer or last", command)
	}
	return display - 1, nil
}

func renderPinReport(out io.Writer, report pinReport) {
	title := "Message pinned"
	if report.Action == "unpin" {
		title = "Message unpinned"
	}
	fmt.Fprintln(out, title)
	fmt.Fprintf(out, "  Session          %s\n", report.SessionID)
	fmt.Fprintf(out, "  Message          %d\n", report.DisplayIndex)
	fmt.Fprintf(out, "  Pinned messages  %s\n", formatIntList(report.PinnedMessages))
}

func formatIntList(values []int) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value+1))
	}
	return strings.Join(parts, ", ")
}

func pasteClipboardPayload(ctx context.Context, req pasteRequest) ([]byte, string, error) {
	data, clipboard, err := readClipboard(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(data) > req.MaxBytes {
		return nil, "", fmt.Errorf("clipboard content is %d bytes, over paste max %d bytes", len(data), req.MaxBytes)
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, "", errors.New("clipboard is empty")
	}
	return data, clipboard, nil
}

func buildPasteReport(req pasteRequest, data []byte, clipboard string, submitted bool) pasteReport {
	return pasteReport{
		Kind:      "paste",
		Action:    "read",
		Status:    "ok",
		SessionID: req.SessionID,
		Bytes:     len(data),
		Lines:     countTextLines(string(data)),
		Clipboard: clipboard,
		Submitted: submitted,
		Preview:   truncateForReport(string(data), 240),
	}
}

func (a *App) copyPayload(req copyRequest) ([]byte, *session.Session, string, error) {
	if req.Scope == "all" {
		format := req.Format
		if strings.TrimSpace(format) == "" {
			format = session.ExportMarkdown
		}
		data, sess, err := a.Sessions.Export(req.SessionID, format)
		if err != nil {
			return nil, nil, "", err
		}
		normalized, _ := session.NormalizeExportFormat(format)
		return data, sess, normalized, nil
	}
	sess, err := a.Sessions.Open(req.SessionID)
	if err != nil {
		return nil, nil, "", err
	}
	text := renderNthAssistantMessage(sess, req.Nth)
	if strings.TrimSpace(text) == "" && req.Nth == 1 {
		text = renderLastSessionMessage(sess)
	}
	if strings.TrimSpace(text) == "" && req.Nth > 1 {
		return nil, nil, "", fmt.Errorf("assistant response %d not found", req.Nth)
	}
	data := []byte(text)
	return data, sess, "text", nil
}

func parseCopyArgs(args []string, overrides config.FlagOverrides) (copyRequest, error) {
	const usage = "codog copy [last|N|all] [--session ID|--resume ID] [--format markdown|json|jsonl|html] [--json]"
	req := copyRequest{Scope: "last", Nth: 1, SessionID: "latest"}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.JSON = true
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "copy", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "copy", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--format" || arg == "--output-format":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "copy", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case arg == "last" || arg == "latest":
			req.Scope = "last"
			req.Nth = 1
		case arg == "all" || arg == "session":
			req.Scope = "all"
			req.Nth = 0
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "copy", Option: arg, Usage: usage}
			}
			nth, err := strconv.Atoi(arg)
			if err != nil {
				return req, unexpectedExtraArgsError{Command: "copy", Args: []string{arg}, Usage: usage}
			}
			if nth < 1 {
				return req, invalidFlagValueError{Flag: "response-index", Value: arg, Message: "copy response index must be greater than zero", Usage: usage}
			}
			req.Scope = "nth"
			req.Nth = nth
		}
	}
	if req.Scope != "all" && strings.TrimSpace(req.Format) != "" && req.Format != "text" {
		return req, invalidFlagValueError{Flag: "--format", Value: req.Format, Message: "copy response only supports text format", Usage: usage}
	}
	if req.Scope == "all" {
		if _, err := session.NormalizeExportFormat(req.Format); err != nil {
			return req, err
		}
	}
	return req, nil
}

func parsePasteArgs(args []string, overrides config.FlagOverrides) (pasteRequest, error) {
	const usage = "codog paste [--print] [--session ID|--resume ID] [--max-bytes N] [--json|--output-format text|json]"
	req := pasteRequest{SessionID: "latest", Format: "text", MaxBytes: 1024 * 1024}
	if overrides.Resume != "" {
		req.SessionID = overrides.Resume
	}
	if overrides.SessionID != "" {
		req.SessionID = overrides.SessionID
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--json":
			req.Format = "json"
			req.JSON = true
		case arg == "--print":
			req.Print = true
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "paste", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--resume":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "paste", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--resume="):
			req.SessionID = strings.TrimPrefix(arg, "--resume=")
		case arg == "--max-bytes":
			index++
			if missingFlagValueAt(args, index) {
				return req, missingFlagValueError{Command: "paste", Flag: arg, Usage: usage}
			}
			value, err := parsePositiveInt(args[index], "paste max bytes")
			if err != nil {
				return req, err
			}
			req.MaxBytes = value
		case strings.HasPrefix(arg, "--max-bytes="):
			value, err := parsePositiveInt(strings.TrimPrefix(arg, "--max-bytes="), "paste max bytes")
			if err != nil {
				return req, err
			}
			req.MaxBytes = value
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "paste", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		default:
			if strings.HasPrefix(arg, "-") {
				return req, unknownOptionError{Command: "paste", Option: arg, Usage: usage}
			}
			return req, unexpectedExtraArgsError{Command: "paste", Args: []string{arg}, Usage: usage}
		}
	}
	normalizedFormat, err := normalizeOutputFormat("paste", req.Format, []string{"text", "json"})
	if err != nil {
		return req, err
	}
	req.Format = normalizedFormat
	req.JSON = req.Format == "json"
	return req, nil
}

func missingFlagValueAt(args []string, index int) bool {
	if index >= len(args) {
		return true
	}
	next := strings.TrimSpace(args[index])
	return next == "" || next == "--output-format" || next == "-o" || strings.HasPrefix(next, "--output-format=")
}

func copyScopeLabel(req copyRequest) string {
	if req.Scope == "nth" {
		return fmt.Sprintf("response %d", req.Nth)
	}
	return req.Scope
}

func renderNthAssistantMessage(sess *session.Session, nth int) string {
	if nth < 1 {
		return ""
	}
	count := 0
	for index := len(sess.Messages) - 1; index >= 0; index-- {
		msg := sess.Messages[index]
		if msg.Role != "assistant" {
			continue
		}
		text := renderMessagePlainText(msg)
		if strings.TrimSpace(text) == "" {
			continue
		}
		count++
		if count == nth {
			return text
		}
	}
	return ""
}

func renderLastSessionMessage(sess *session.Session) string {
	for index := len(sess.Messages) - 1; index >= 0; index-- {
		msg := sess.Messages[index]
		if msg.Role == "assistant" {
			return renderMessagePlainText(msg)
		}
	}
	if len(sess.Messages) == 0 {
		return ""
	}
	return renderMessagePlainText(sess.Messages[len(sess.Messages)-1])
}

func renderMessagePlainText(msg anthropic.Message) string {
	var builder strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				builder.WriteString(strings.TrimSpace(block.Text))
				builder.WriteString("\n")
			}
		case "tool_result":
			if strings.TrimSpace(block.Content) != "" {
				builder.WriteString(strings.TrimSpace(block.Content))
				builder.WriteString("\n")
			}
		}
	}
	text := strings.TrimSpace(builder.String())
	if text == "" {
		return ""
	}
	return text + "\n"
}

func countTextLines(value string) int {
	if value == "" {
		return 0
	}
	lines := strings.Count(value, "\n")
	if !strings.HasSuffix(value, "\n") {
		lines++
	}
	return lines
}

func writeSystemClipboard(ctx context.Context, data []byte) (string, error) {
	candidates := clipboardCommands()
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, candidate[0], candidate[1:]...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return "", err
		}
		if err := cmd.Start(); err != nil {
			return "", err
		}
		if _, err := stdin.Write(data); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			return "", err
		}
		if err := stdin.Close(); err != nil {
			_ = cmd.Wait()
			return "", err
		}
		if err := cmd.Wait(); err != nil {
			return "", err
		}
		return candidate[0], nil
	}
	return "", errors.New("no clipboard command found")
}

func readSystemClipboard(ctx context.Context) ([]byte, string, error) {
	candidates := clipboardReadCommands()
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate[0]); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, candidate[0], candidate[1:]...)
		data, err := cmd.Output()
		if err != nil {
			return nil, "", err
		}
		return data, candidate[0], nil
	}
	return nil, "", errors.New("no clipboard command found")
}

func readSystemClipboardImage(ctx context.Context) (clipboardImage, error) {
	switch runtime.GOOS {
	case "darwin":
		return readDarwinClipboardImage(ctx)
	case "windows":
		return readWindowsClipboardImage(ctx)
	default:
		return readUnixClipboardImage(ctx)
	}
}

func readDarwinClipboardImage(ctx context.Context) (clipboardImage, error) {
	if _, err := exec.LookPath("osascript"); err != nil {
		return clipboardImage{}, errNoClipboardImage
	}
	file, err := os.CreateTemp("", "codog-clipboard-*.png")
	if err != nil {
		return clipboardImage{}, err
	}
	path := file.Name()
	_ = file.Close()
	defer func() { _ = os.Remove(path) }()
	escapedPath := strings.ReplaceAll(path, `"`, `\"`)
	args := []string{
		"-e", `set outputPath to "` + escapedPath + `"`,
		"-e", `set imageData to the clipboard as «class PNGf»`,
		"-e", `set outputFile to open for access POSIX file outputPath with write permission`,
		"-e", `set eof outputFile to 0`,
		"-e", `write imageData to outputFile`,
		"-e", `close access outputFile`,
	}
	if err := exec.CommandContext(ctx, "osascript", args...).Run(); err != nil {
		return clipboardImage{}, errNoClipboardImage
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return clipboardImage{}, err
	}
	if !looksLikeClipboardImage(data, "image/png") {
		return clipboardImage{}, errNoClipboardImage
	}
	return clipboardImage{Data: data, MediaType: "image/png", Extension: ".png", Clipboard: "osascript"}, nil
}

func readWindowsClipboardImage(ctx context.Context) (clipboardImage, error) {
	command, ok := firstAvailableCommand([]string{"powershell", "pwsh"})
	if !ok {
		return clipboardImage{}, errNoClipboardImage
	}
	file, err := os.CreateTemp("", "codog-clipboard-*.png")
	if err != nil {
		return clipboardImage{}, err
	}
	path := file.Name()
	_ = file.Close()
	defer func() { _ = os.Remove(path) }()
	script := `$ErrorActionPreference = 'Stop'; Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; if (-not [System.Windows.Forms.Clipboard]::ContainsImage()) { exit 3 }; $img = [System.Windows.Forms.Clipboard]::GetImage(); $img.Save('` + strings.ReplaceAll(path, `'`, `''`) + `', [System.Drawing.Imaging.ImageFormat]::Png)`
	if err := exec.CommandContext(ctx, command, "-NoProfile", "-Command", script).Run(); err != nil {
		return clipboardImage{}, errNoClipboardImage
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return clipboardImage{}, err
	}
	if !looksLikeClipboardImage(data, "image/png") {
		return clipboardImage{}, errNoClipboardImage
	}
	return clipboardImage{Data: data, MediaType: "image/png", Extension: ".png", Clipboard: command}, nil
}

func readUnixClipboardImage(ctx context.Context) (clipboardImage, error) {
	candidates := []struct {
		Command   []string
		MediaType string
		Extension string
	}{
		{Command: []string{"wl-paste", "--type", "image/png"}, MediaType: "image/png", Extension: ".png"},
		{Command: []string{"wl-paste", "--type", "image/jpeg"}, MediaType: "image/jpeg", Extension: ".jpg"},
		{Command: []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-out"}, MediaType: "image/png", Extension: ".png"},
		{Command: []string{"xclip", "-selection", "clipboard", "-t", "image/jpeg", "-out"}, MediaType: "image/jpeg", Extension: ".jpg"},
	}
	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate.Command[0]); err != nil {
			continue
		}
		data, err := exec.CommandContext(ctx, candidate.Command[0], candidate.Command[1:]...).Output()
		if err != nil || !looksLikeClipboardImage(data, candidate.MediaType) {
			continue
		}
		return clipboardImage{Data: data, MediaType: candidate.MediaType, Extension: candidate.Extension, Clipboard: candidate.Command[0]}, nil
	}
	return clipboardImage{}, errNoClipboardImage
}

func firstAvailableCommand(commands []string) (string, bool) {
	for _, command := range commands {
		if _, err := exec.LookPath(command); err == nil {
			return command, true
		}
	}
	return "", false
}

func looksLikeClipboardImage(data []byte, mediaType string) bool {
	if len(data) == 0 {
		return false
	}
	detected := cleanMediaType(http.DetectContentType(data))
	if isPromptImageMediaType(detected) {
		return true
	}
	return isPromptImageMediaType(mediaType)
}

func clipboardCommands() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip"}}
	default:
		return [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	}
}

func clipboardReadCommands() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pbpaste"}}
	case "windows":
		return [][]string{
			{"powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw"},
			{"pwsh", "-NoProfile", "-Command", "Get-Clipboard -Raw"},
		}
	default:
		return [][]string{{"wl-paste"}, {"xclip", "-selection", "clipboard", "-out"}, {"xsel", "--clipboard", "--output"}}
	}
}

func (a *App) handleExportSlash(args []string, sess *session.Session) {
	req, err := parseExportArgs(args, sess.ID)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if req.Output == "" {
		req.Output = session.DefaultExportFilenameForFormat(sess, req.Format)
	}
	data, exported, err := a.Sessions.Export(req.SessionID, req.Format)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	path, err := a.resolveWorkspaceOutputPath(req.Output)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	if err := session.ValidateExportOutputPath(path); err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	path, err = writeUniqueExportFile(path, data)
	if err != nil {
		fmt.Fprintln(a.Err, "error:", err)
		return
	}
	fmt.Fprintf(a.Err, "exported session %s to %s (%d messages)\n", exported.ID, path, len(exported.Messages))
}

func writeUniqueExportFile(path string, data []byte) (string, error) {
	for attempt := 0; attempt < 10000; attempt++ {
		candidate := exportCollisionCandidate(path, attempt)
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return candidate, err
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return candidate, err
		}
		if err := file.Close(); err != nil {
			return candidate, err
		}
		return candidate, nil
	}
	return path, fmt.Errorf("no available export filename for %s", path)
}

func exportCollisionCandidate(path string, attempt int) string {
	if attempt <= 0 {
		return path
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		stem = base
		ext = ""
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, attempt+1, ext))
}

func parseExportArgs(args []string, defaultSession string) (exportRequest, error) {
	req := exportRequest{SessionID: defaultSession, Format: session.ExportMarkdown}
	usage := "codog export [PATH] [--session ID] [--output PATH] [--format markdown|json|jsonl|html]"
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--session":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "export", Flag: arg, Usage: usage}
			}
			req.SessionID = args[index]
		case strings.HasPrefix(arg, "--session="):
			req.SessionID = strings.TrimPrefix(arg, "--session=")
		case arg == "--output" || arg == "-o":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "export", Flag: arg, Usage: usage}
			}
			req.Output = args[index]
		case strings.HasPrefix(arg, "--output="):
			req.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--format" || arg == "--output-format":
			index++
			if index >= len(args) {
				return req, missingFlagValueError{Command: "export", Flag: arg, Usage: usage}
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--format="):
			req.Format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: "export", Option: arg, Usage: usage}
		default:
			if req.Output != "" {
				return req, unexpectedExtraArgsError{Command: "export", Args: []string{arg}, Usage: usage}
			}
			req.Output = arg
		}
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = "latest"
	}
	if _, err := session.NormalizeExportFormat(req.Format); err != nil {
		return req, err
	}
	return req, nil
}

func (a *App) resolveOutputPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	base := a.Workspace
	if base == "" {
		base = "."
	}
	return filepath.Join(base, path)
}

func (a *App) resolveWorkspaceOutputPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("output path is required")
	}
	resolved := a.resolveOutputPath(trimmed)
	if filepath.IsAbs(trimmed) {
		return resolved, nil
	}
	base := a.Workspace
	if strings.TrimSpace(base) == "" {
		base = "."
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absBase, absResolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("output path %q escapes workspace %q", trimmed, absBase)
	}
	return resolved, nil
}

func (a *App) handleSessionSlash(args []string, sess *session.Session) {
	action := ""
	if len(args) > 0 {
		action = normalizeSessionAction(args[0])
	}
	if len(args) == 0 || action == "list" {
		if len(args) > 0 {
			args = args[1:]
		}
		if err := a.ListSessionsWithActive(args, sess.ID); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
		return
	}
	switch action {
	case "exists":
		if err := a.SessionExists(args[1:], sess.ID); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
	case "switch":
		if len(args) < 2 {
			fmt.Fprintln(a.Err, "usage: /session switch ID")
			return
		}
		next, err := a.Sessions.OpenExisting(args[1])
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		*sess = *next
		fmt.Fprintf(a.Err, "session switched: %s\n", sess.ID)
	case "fork":
		a.sessionSlashFork(args[1:], sess)
	case "rename":
		a.sessionSlashRename(args[1:], sess)
	case "prune":
		a.sessionSlashPrune(args[1:], sess.ID)
	case "repair":
		if err := a.RepairSessions(args[1:]); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "pin", "unpin":
		req, err := parseSessionPinArgs("/session "+action, args[1:], sess.ID, "text")
		if err != nil {
			fmt.Fprintln(a.Err, "error:", err)
			return
		}
		if err := a.runPinRequest(action, req); err != nil {
			fmt.Fprintln(a.Err, "error:", err)
		}
	case "delete":
		a.sessionSlashDelete(args[1:], sess)
	default:
		fmt.Fprintf(a.Err, "unknown /session action: %s\n", args[0])
	}
}

func (a *App) sessionSlashFork(args []string, sess *session.Session) {
	req, err := parseSessionForkArgs("/session fork", args, sess.ID, "text")
	if a.writeSessionSlashError(err) {
		return
	}
	report, next, err := a.forkSessionWithReport(req.SourceID, req.BranchName)
	if a.writeSessionSlashError(err) {
		return
	}
	*sess = *next
	if req.Format == "json" {
		_ = renderJSONValue(a.Out, report)
		return
	}
	renderSessionForkText(a.Err, report)
}

func (a *App) sessionSlashRename(args []string, sess *session.Session) {
	req, err := parseSessionRenameArgs("/session rename", args, sess.ID, "text")
	if a.writeSessionSlashError(err) {
		return
	}
	report, err := a.renameSessionWithReport(req.OldID, req.NewID)
	if a.writeSessionSlashError(err) {
		return
	}
	next, err := a.Sessions.Open(report.NewSessionID)
	if a.writeSessionSlashError(err) {
		return
	}
	*sess = *next
	if req.Format == "json" {
		_ = renderJSONValue(a.Out, report)
		return
	}
	renderSessionRenameText(a.Err, report)
}

func (a *App) sessionSlashPrune(args []string, activeID string) {
	req, err := parseSessionPruneArgs("/session prune", args, "text")
	if a.writeSessionSlashError(err) {
		return
	}
	report, err := a.pruneSessionsWithReport(req, activeID)
	if a.writeSessionSlashError(err) {
		return
	}
	if req.Format == "json" {
		_ = renderJSONValue(a.Out, report)
		return
	}
	renderSessionPruneText(a.Err, report)
}

func (a *App) sessionSlashDelete(args []string, sess *session.Session) {
	req, err := parseSessionDeleteArgs("/session delete", args)
	if a.writeSessionSlashError(err) {
		return
	}
	if !req.Force {
		fmt.Fprintf(a.Err, "delete: confirmation required; rerun with /session delete %s --force\n", req.ID)
		return
	}
	target, err := a.Sessions.OpenExisting(req.ID)
	if a.writeSessionSlashError(err) {
		return
	}
	if target.ID == sess.ID || target.Path == sess.Path {
		fmt.Fprintf(a.Err, "delete: refusing to delete the active session %q\n", target.ID)
		return
	}
	report, err := a.deleteSessionWithReport(target.ID)
	if a.writeSessionSlashError(err) {
		return
	}
	if req.Format == "json" {
		_ = renderJSONValue(a.Out, report)
		return
	}
	renderSessionDeleteText(a.Err, report)
}

func (a *App) writeSessionSlashError(err error) bool {
	if err == nil {
		return false
	}
	fmt.Fprintln(a.Err, "error:", err)
	return true
}

type sessionDeleteRequest struct {
	ID     string
	Force  bool
	Format string
}

func parseSessionDeleteArgs(command string, args []string) (sessionDeleteRequest, error) {
	req := sessionDeleteRequest{Format: "text"}
	usage := command + " ID --force"
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		switch {
		case arg == "":
		case arg == "--force" || arg == "-f":
			req.Force = true
		case arg == "--json":
			req.Format = "json"
		case arg == "--output-format" || arg == "-o":
			index++
			if index >= len(args) {
				return req, fmt.Errorf("%s output format is required", command)
			}
			req.Format = args[index]
		case strings.HasPrefix(arg, "--output-format="):
			req.Format = strings.TrimPrefix(arg, "--output-format=")
		case strings.HasPrefix(arg, "-"):
			return req, unknownOptionError{Command: command, Option: arg, Usage: usage}
		default:
			if req.ID != "" {
				return req, unexpectedExtraArgsError{Command: command, Args: []string{arg}, Usage: usage}
			}
			req.ID = arg
		}
	}
	if req.ID == "" {
		return req, fmt.Errorf("usage: %s", usage)
	}
	switch req.Format {
	case "text", "json":
	default:
		return req, fmt.Errorf("unknown %s output format %q", command, req.Format)
	}
	return req, nil
}

func (a *App) SessionsCommand(args []string) error {
	action := ""
	if len(args) > 0 {
		action = normalizeSessionAction(args[0])
	}
	if len(args) == 0 || action == "list" {
		if len(args) > 0 {
			args = args[1:]
		}
		return a.ListSessionsWithActive(args, "")
	}
	switch action {
	case "show":
		showArgs := args[1:]
		if !argsHaveOutputFormat(showArgs) {
			showArgs = append(append([]string(nil), showArgs...), "--json")
		}
		return a.SessionShow(showArgs)
	case "exists":
		existsArgs := args[1:]
		if !argsHaveOutputFormat(existsArgs) {
			existsArgs = append(append([]string(nil), existsArgs...), "--json")
		}
		return a.SessionExists(existsArgs, "")
	case "search":
		return a.sessionsSearch(args[1:])
	case "audit":
		return a.sessionsAudit(args[1:])
	case "repair":
		return a.RepairSessions(args[1:])
	case "export":
		return a.SessionExport(args[1:])
	case "import":
		return a.sessionsImport(args[1:])
	case "fork":
		return a.sessionsFork(args[1:])
	case "switch":
		return a.sessionsSwitch(args[1:])
	case "rename":
		return a.sessionsRename(args[1:])
	case "prune":
		return a.sessionsPrune(args[1:])
	case "pin", "unpin":
		req, err := parseSessionPinArgs("codog sessions "+action, args[1:], "", "json")
		if err != nil {
			return err
		}
		return a.runPinRequest(action, req)
	case "delete":
		return a.sessionsDelete(args[1:])
	default:
		return sessionsActionError{Action: args[0]}
	}
}

func (a *App) sessionsSearch(args []string) error {
	req, err := parseSessionSearchArgs("codog sessions search", args, "json")
	if err != nil {
		return err
	}
	report, err := a.searchSessionsWithReport(req)
	if err != nil {
		return err
	}
	return renderSessionSearchReport(a.Out, report, req.Format)
}

func (a *App) sessionsAudit(args []string) error {
	format, err := parseSimpleOutputFormat("sessions audit", args)
	if err != nil {
		return err
	}
	report, err := a.auditSessionsWithReport()
	if err != nil {
		return err
	}
	if format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderSessionAuditText(a.Out, report)
	return nil
}

func (a *App) sessionsImport(args []string) error {
	req, err := parseSessionImportArgs("codog sessions import", args, "json")
	if err != nil {
		return err
	}
	report, err := a.importSessionWithReport(req)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderSessionImportText(a.Out, report)
	return nil
}

func (a *App) sessionsFork(args []string) error {
	req, err := parseSessionForkArgs("codog sessions fork", args, "", "json")
	if err != nil {
		return err
	}
	report, _, err := a.forkSessionWithReport(req.SourceID, req.BranchName)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderSessionForkText(a.Out, report)
	return nil
}

func (a *App) sessionsSwitch(args []string) error {
	req, err := parseSessionSwitchArgs("codog sessions switch", args, "json")
	if err != nil {
		return err
	}
	report, err := a.switchSessionWithReport("", req.ID)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderSessionSwitchText(a.Out, report)
	return nil
}

func (a *App) sessionsRename(args []string) error {
	req, err := parseSessionRenameArgs("codog sessions rename", args, "", "json")
	if err != nil {
		return err
	}
	report, err := a.renameSessionWithReport(req.OldID, req.NewID)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderSessionRenameText(a.Out, report)
	return nil
}

func (a *App) sessionsPrune(args []string) error {
	req, err := parseSessionPruneArgs("codog sessions prune", args, "json")
	if err != nil {
		return err
	}
	report, err := a.pruneSessionsWithReport(req, "")
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderSessionPruneText(a.Out, report)
	return nil
}

func (a *App) sessionsDelete(args []string) error {
	if !argsHaveOutputFormat(args) {
		args = append(append([]string(nil), args...), "--json")
	}
	req, err := parseSessionDeleteArgs("codog sessions delete", args)
	if err != nil {
		return err
	}
	if !req.Force {
		return fmt.Errorf("delete: confirmation required; rerun with codog sessions delete %s --force", req.ID)
	}
	report, err := a.deleteSessionWithReport(req.ID)
	if err != nil {
		return err
	}
	if req.Format == "json" {
		return renderJSONValue(a.Out, report)
	}
	renderSessionDeleteText(a.Out, report)
	return nil
}

func renderSessionSearchReport(out io.Writer, report sessionSearchReport, format string) error {
	if format == "json" {
		return renderJSONValue(out, report)
	}
	renderSessionSearchText(out, report)
	return nil
}

func renderJSONValue(out io.Writer, value any) error {
	data, _ := json.MarshalIndent(value, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
}
