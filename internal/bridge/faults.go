package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxBridgeFaultEvents = 100

type FaultEvent struct {
	ID          string    `json:"id"`
	Action      string    `json:"action"`
	Args        []string  `json:"args,omitempty"`
	Category    string    `json:"category"`
	Severity    string    `json:"severity"`
	Recoverable bool      `json:"recoverable"`
	Message     string    `json:"message"`
	Remediation string    `json:"remediation"`
	CreatedAt   time.Time `json:"created_at"`
}

type faultLog struct {
	Events []FaultEvent `json:"events"`
}

func (s Server) BridgeFaults() ([]FaultEvent, error) {
	log, err := s.loadBridgeFaultLog()
	if err != nil {
		return nil, err
	}
	return append([]FaultEvent(nil), log.Events...), nil
}

func (s Server) RecordBridgeFault(action string, args []string) (FaultEvent, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return FaultEvent{}, errors.New("bridge fault action is required")
	}
	cleanArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if trimmed := strings.TrimSpace(arg); trimmed != "" {
			cleanArgs = append(cleanArgs, trimmed)
		}
	}
	log, err := s.loadBridgeFaultLog()
	if err != nil {
		return FaultEvent{}, err
	}
	now := time.Now().UTC()
	details := bridgeFaultDetails(action, cleanArgs)
	event := FaultEvent{
		ID:          "fault-" + now.Format("20060102T150405.000000000Z"),
		Action:      action,
		Args:        cleanArgs,
		Category:    details.Category,
		Severity:    details.Severity,
		Recoverable: details.Recoverable,
		Message:     details.Message,
		Remediation: details.Remediation,
		CreatedAt:   now,
	}
	log.Events = append(log.Events, event)
	if len(log.Events) > maxBridgeFaultEvents {
		log.Events = append([]FaultEvent(nil), log.Events[len(log.Events)-maxBridgeFaultEvents:]...)
	}
	if err := s.saveBridgeFaultLog(log); err != nil {
		return FaultEvent{}, err
	}
	return event, nil
}

func (s Server) ClearBridgeFaults() error {
	path, err := s.bridgeFaultLogPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s Server) bridgeFaultsList() (any, error) {
	events, err := s.BridgeFaults()
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "bridge_faults", "total": len(events), "events": events}, nil
}

func (s Server) bridgeFaultsRecord(params json.RawMessage) (any, error) {
	var payload struct {
		Action string   `json:"action"`
		Args   []string `json:"args"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, err
	}
	event, err := s.RecordBridgeFault(payload.Action, payload.Args)
	if err != nil {
		return nil, err
	}
	events, err := s.BridgeFaults()
	if err != nil {
		return nil, err
	}
	return map[string]any{"kind": "bridge_faults", "total": len(events), "recorded": event, "events": events}, nil
}

func (s Server) bridgeFaultsClear() (any, error) {
	if err := s.ClearBridgeFaults(); err != nil {
		return nil, err
	}
	return map[string]any{"kind": "bridge_faults", "cleared": true, "total": 0, "events": []FaultEvent{}}, nil
}

func (s Server) loadBridgeFaultLog() (faultLog, error) {
	path, err := s.bridgeFaultLogPath()
	if err != nil {
		return faultLog{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return faultLog{}, nil
		}
		return faultLog{}, err
	}
	var log faultLog
	if err := json.Unmarshal(data, &log); err != nil {
		return faultLog{}, err
	}
	return log, nil
}

func (s Server) saveBridgeFaultLog(log faultLog) error {
	path, err := s.bridgeFaultLogPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func (s Server) bridgeFaultLogPath() (string, error) {
	if strings.TrimSpace(s.ConfigHome) == "" {
		return "", errors.New("config home is required")
	}
	return filepath.Join(s.ConfigHome, "bridge", "faults.json"), nil
}

type bridgeFaultDetailsReport struct {
	Category    string
	Severity    string
	Recoverable bool
	Message     string
	Remediation string
}

func bridgeFaultDetails(action string, args []string) bridgeFaultDetailsReport {
	switch action {
	case "poll":
		if len(args) > 0 {
			return bridgeFaultDetailsReport{
				Category:    "polling",
				Severity:    bridgePollingSeverity(args[0]),
				Recoverable: true,
				Message:     "Recorded bridge polling diagnostic response " + args[0] + ".",
				Remediation: "Inspect editor bridge status, verify the editor bridge endpoint, then retry polling.",
			}
		}
		return bridgeFaultDetailsReport{
			Category:    "polling",
			Severity:    "error",
			Recoverable: true,
			Message:     "Recorded bridge polling diagnostic failure.",
			Remediation: "Check whether the editor bridge is running and reachable before retrying.",
		}
	case "error":
		if len(args) > 0 {
			return bridgeFaultDetailsReport{
				Category:    "runtime_error",
				Severity:    "error",
				Recoverable: true,
				Message:     "Recorded bridge diagnostic error: " + strings.Join(args, " ") + ".",
				Remediation: "Open bridge logs, inspect the recorded error, and restart the bridge if the failure persists.",
			}
		}
		return bridgeFaultDetailsReport{
			Category:    "runtime_error",
			Severity:    "error",
			Recoverable: true,
			Message:     "Recorded bridge diagnostic error.",
			Remediation: "Open bridge logs and restart the bridge if the failure persists.",
		}
	case "drop", "disconnect":
		return bridgeFaultDetailsReport{
			Category:    "connection",
			Severity:    "error",
			Recoverable: true,
			Message:     "Recorded bridge connection drop.",
			Remediation: "Reconnect the editor bridge and confirm the trust token still matches.",
		}
	case "latency", "delay":
		if len(args) > 0 {
			return bridgeFaultDetailsReport{
				Category:    "latency",
				Severity:    "warn",
				Recoverable: true,
				Message:     "Recorded bridge latency diagnostic " + args[0] + ".",
				Remediation: "Check editor bridge responsiveness and background task load.",
			}
		}
		return bridgeFaultDetailsReport{
			Category:    "latency",
			Severity:    "warn",
			Recoverable: true,
			Message:     "Recorded bridge latency diagnostic.",
			Remediation: "Check editor bridge responsiveness and background task load.",
		}
	case "timeout":
		return bridgeFaultDetailsReport{
			Category:    "timeout",
			Severity:    "error",
			Recoverable: true,
			Message:     "Recorded bridge timeout.",
			Remediation: "Retry the bridge request, then restart the bridge if timeouts continue.",
		}
	default:
		if len(args) > 0 {
			return bridgeFaultDetailsReport{
				Category:    "diagnostic",
				Severity:    "info",
				Recoverable: true,
				Message:     "Recorded bridge diagnostic event " + action + " " + strings.Join(args, " ") + ".",
				Remediation: "Review the event arguments and bridge state for follow-up.",
			}
		}
		return bridgeFaultDetailsReport{
			Category:    "diagnostic",
			Severity:    "info",
			Recoverable: true,
			Message:     "Recorded bridge diagnostic event " + action + ".",
			Remediation: "Review bridge state for follow-up.",
		}
	}
}

func bridgePollingSeverity(status string) string {
	status = strings.TrimSpace(status)
	if strings.HasPrefix(status, "2") || strings.HasPrefix(status, "3") {
		return "info"
	}
	if strings.HasPrefix(status, "4") {
		return "warn"
	}
	return "error"
}
