package g004conformance

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	BundleSchemaVersion = "g004.contract.bundle.v1"
	ReportSchemaVersion = "g004.report.v1"
)

type Error struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Result struct {
	Kind       string  `json:"kind"`
	Schema     string  `json:"schema"`
	Valid      bool    `json:"valid"`
	ErrorCount int     `json:"error_count"`
	Errors     []Error `json:"errors,omitempty"`
}

func ValidateJSON(data []byte) (Result, error) {
	var bundle any
	if err := json.Unmarshal(data, &bundle); err != nil {
		return Result{}, err
	}
	object, ok := bundle.(map[string]any)
	if !ok {
		return Result{}, fmt.Errorf("g004 contract bundle must be a JSON object")
	}
	return Validate(object), nil
}

func Validate(bundle map[string]any) Result {
	var errors []Error
	requireStringEq(bundle, "/schemaVersion", BundleSchemaVersion, &errors)
	validateLaneEvents(getMapValue(bundle, "laneEvents"), "/laneEvents", &errors)
	validateReports(getMapValue(bundle, "reports"), "/reports", &errors)
	validateApprovalTokens(getMapValue(bundle, "approvalTokens"), "/approvalTokens", &errors)
	return Result{
		Kind:       "g004_conformance",
		Schema:     BundleSchemaVersion,
		Valid:      len(errors) == 0,
		ErrorCount: len(errors),
		Errors:     errors,
	}
}

func validateLaneEvents(value any, path string, errors *[]Error) {
	events, ok := nonEmptyArray(value, path, errors)
	if !ok {
		return
	}
	var previousSeq *float64
	for index, event := range events {
		eventMap, ok := objectAt(event, fmt.Sprintf("%s/%d", path, index), errors)
		if !ok {
			continue
		}
		base := fmt.Sprintf("%s/%d", path, index)
		requireNonEmptyStringAt(eventMap, "/event", base+"/event", errors)
		requireNonEmptyStringAt(eventMap, "/status", base+"/status", errors)
		requireNonEmptyStringAt(eventMap, "/emittedAt", base+"/emittedAt", errors)
		requireNonEmptyStringAt(eventMap, "/metadata/provenance", base+"/metadata/provenance", errors)
		requireNonEmptyStringAt(eventMap, "/metadata/emitterIdentity", base+"/metadata/emitterIdentity", errors)
		requireNonEmptyStringAt(eventMap, "/metadata/environmentLabel", base+"/metadata/environmentLabel", errors)
		if seq, ok := numberAt(eventMap, "/metadata/seq"); ok {
			if previousSeq != nil && seq <= *previousSeq {
				*errors = append(*errors, Error{Path: base + "/metadata/seq", Message: "sequence must be strictly increasing"})
			}
			current := seq
			previousSeq = &current
		} else {
			*errors = append(*errors, Error{Path: base + "/metadata/seq", Message: "required u64 field missing"})
		}
		if eventName, ok := stringAt(eventMap, "/event"); ok && isTerminalEvent(eventName) {
			requireNonEmptyStringAt(eventMap, "/metadata/eventFingerprint", base+"/metadata/eventFingerprint", errors)
		}
	}
}

func validateReports(value any, path string, errors *[]Error) {
	reports, ok := nonEmptyArray(value, path, errors)
	if !ok {
		return
	}
	for index, report := range reports {
		reportMap, ok := objectAt(report, fmt.Sprintf("%s/%d", path, index), errors)
		if !ok {
			continue
		}
		base := fmt.Sprintf("%s/%d", path, index)
		requireStringEqAt(reportMap, "/schemaVersion", base+"/schemaVersion", ReportSchemaVersion, errors)
		requireNonEmptyStringAt(reportMap, "/reportId", base+"/reportId", errors)
		requireNonEmptyStringAt(reportMap, "/identity/contentHash", base+"/identity/contentHash", errors)
		requireNonEmptyStringAt(reportMap, "/projection/provenance", base+"/projection/provenance", errors)
		requireNonEmptyStringAt(reportMap, "/redaction/provenance", base+"/redaction/provenance", errors)
		nonEmptyArray(getPath(reportMap, "/consumerCapabilities"), base+"/consumerCapabilities", errors)
		validateFindings(getPath(reportMap, "/findings"), base+"/findings", errors)
		validateFieldDeltas(getPath(reportMap, "/fieldDeltas"), base+"/fieldDeltas", errors)
	}
}

func validateFindings(value any, path string, errors *[]Error) {
	findings, ok := nonEmptyArray(value, path, errors)
	if !ok {
		return
	}
	for index, finding := range findings {
		findingMap, ok := objectAt(finding, fmt.Sprintf("%s/%d", path, index), errors)
		if !ok {
			continue
		}
		base := fmt.Sprintf("%s/%d", path, index)
		requireOneOfAt(findingMap, "/kind", base+"/kind", []string{"fact", "hypothesis", "negative_evidence"}, errors)
		requireOneOfAt(findingMap, "/confidence", base+"/confidence", []string{"low", "medium", "high"}, errors)
		requireNonEmptyStringAt(findingMap, "/statement", base+"/statement", errors)
	}
}

func validateFieldDeltas(value any, path string, errors *[]Error) {
	deltas, ok := nonEmptyArray(value, path, errors)
	if !ok {
		return
	}
	for index, delta := range deltas {
		deltaMap, ok := objectAt(delta, fmt.Sprintf("%s/%d", path, index), errors)
		if !ok {
			continue
		}
		base := fmt.Sprintf("%s/%d", path, index)
		requireNonEmptyStringAt(deltaMap, "/field", base+"/field", errors)
		requireNonEmptyStringAt(deltaMap, "/previousHash", base+"/previousHash", errors)
		requireNonEmptyStringAt(deltaMap, "/currentHash", base+"/currentHash", errors)
		requireNonEmptyStringAt(deltaMap, "/attribution", base+"/attribution", errors)
	}
}

func validateApprovalTokens(value any, path string, errors *[]Error) {
	tokens, ok := nonEmptyArray(value, path, errors)
	if !ok {
		return
	}
	for index, token := range tokens {
		tokenMap, ok := objectAt(token, fmt.Sprintf("%s/%d", path, index), errors)
		if !ok {
			continue
		}
		base := fmt.Sprintf("%s/%d", path, index)
		requireNonEmptyStringAt(tokenMap, "/tokenId", base+"/tokenId", errors)
		requireNonEmptyStringAt(tokenMap, "/owner", base+"/owner", errors)
		requireNonEmptyStringAt(tokenMap, "/scope", base+"/scope", errors)
		requireNonEmptyStringAt(tokenMap, "/issuedAt", base+"/issuedAt", errors)
		requireBoolTrueAt(tokenMap, "/oneTimeUse", base+"/oneTimeUse", errors)
		requireNonEmptyStringAt(tokenMap, "/replayPreventionNonce", base+"/replayPreventionNonce", errors)
		validateDelegationChain(getPath(tokenMap, "/delegationChain"), base+"/delegationChain", errors)
	}
}

func validateDelegationChain(value any, path string, errors *[]Error) {
	chain, ok := nonEmptyArray(value, path, errors)
	if !ok {
		return
	}
	for index, hop := range chain {
		hopMap, ok := objectAt(hop, fmt.Sprintf("%s/%d", path, index), errors)
		if !ok {
			continue
		}
		base := fmt.Sprintf("%s/%d", path, index)
		requireNonEmptyStringAt(hopMap, "/from", base+"/from", errors)
		requireNonEmptyStringAt(hopMap, "/to", base+"/to", errors)
		requireNonEmptyStringAt(hopMap, "/action", base+"/action", errors)
		requireNonEmptyStringAt(hopMap, "/at", base+"/at", errors)
	}
}

func nonEmptyArray(value any, path string, errors *[]Error) ([]any, bool) {
	array, ok := value.([]any)
	if !ok {
		*errors = append(*errors, Error{Path: path, Message: "required array field missing"})
		return nil, false
	}
	if len(array) == 0 {
		*errors = append(*errors, Error{Path: path, Message: "array must not be empty"})
		return nil, false
	}
	return array, true
}

func objectAt(value any, path string, errors *[]Error) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, Error{Path: path, Message: "required object field missing"})
		return nil, false
	}
	return object, true
}

func requireStringEq(root map[string]any, pointer string, expected string, errors *[]Error) {
	requireStringEqAt(root, pointer, pointer, expected, errors)
}

func requireStringEqAt(root map[string]any, pointer string, errorPath string, expected string, errors *[]Error) {
	if actual, ok := stringAt(root, pointer); ok {
		if actual != expected {
			*errors = append(*errors, Error{Path: errorPath, Message: fmt.Sprintf("expected '%s', got '%s'", expected, actual)})
		}
		return
	}
	*errors = append(*errors, Error{Path: errorPath, Message: "required string field missing"})
}

func requireNonEmptyStringAt(root map[string]any, pointer string, errorPath string, errors *[]Error) {
	value, ok := stringAt(root, pointer)
	if !ok {
		*errors = append(*errors, Error{Path: errorPath, Message: "required string field missing"})
		return
	}
	if strings.TrimSpace(value) == "" {
		*errors = append(*errors, Error{Path: errorPath, Message: "string must not be empty"})
	}
}

func requireOneOfAt(root map[string]any, pointer string, errorPath string, allowed []string, errors *[]Error) {
	value, ok := stringAt(root, pointer)
	if !ok {
		*errors = append(*errors, Error{Path: errorPath, Message: "required string field missing"})
		return
	}
	for _, candidate := range allowed {
		if value == candidate {
			return
		}
	}
	*errors = append(*errors, Error{Path: errorPath, Message: fmt.Sprintf("'%s' is not one of %s", value, strings.Join(allowed, ", "))})
}

func requireBoolTrueAt(root map[string]any, pointer string, errorPath string, errors *[]Error) {
	value, ok := getPath(root, pointer).(bool)
	if !ok {
		*errors = append(*errors, Error{Path: errorPath, Message: "required boolean field missing"})
		return
	}
	if !value {
		*errors = append(*errors, Error{Path: errorPath, Message: "must be true"})
	}
}

func stringAt(root map[string]any, pointer string) (string, bool) {
	value, ok := getPath(root, pointer).(string)
	return value, ok
}

func numberAt(root map[string]any, pointer string) (float64, bool) {
	value, ok := getPath(root, pointer).(float64)
	if !ok || value < 0 || value != float64(uint64(value)) {
		return 0, false
	}
	return value, true
}

func getPath(root map[string]any, pointer string) any {
	if value, ok := pointerLookup(root, pointer); ok {
		return value
	}
	segments := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for index := 1; index < len(segments); index++ {
		relative := "/" + strings.Join(segments[index:], "/")
		if value, ok := pointerLookup(root, relative); ok {
			return value
		}
	}
	return nil
}

func pointerLookup(root any, pointer string) (any, bool) {
	current := root
	if pointer == "" || pointer == "/" {
		return current, true
	}
	for _, raw := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, ok := object[segment]
		if !ok {
			return nil, false
		}
		current = value
	}
	return current, true
}

func getMapValue(root map[string]any, key string) any {
	if root == nil {
		return nil
	}
	return root[key]
}

func isTerminalEvent(event string) bool {
	switch event {
	case "lane.finished", "lane.failed", "lane.merged", "lane.superseded", "lane.closed":
		return true
	default:
		return false
	}
}
