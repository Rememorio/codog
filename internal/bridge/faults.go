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
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Args      []string  `json:"args,omitempty"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
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
	event := FaultEvent{
		ID:        "fault-" + now.Format("20060102T150405.000000000Z"),
		Action:    action,
		Args:      cleanArgs,
		Message:   bridgeFaultMessage(action, cleanArgs),
		CreatedAt: now,
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

func bridgeFaultMessage(action string, args []string) string {
	switch action {
	case "poll":
		if len(args) > 0 {
			return "Recorded simulated bridge polling response " + args[0] + "."
		}
		return "Recorded simulated bridge polling failure."
	case "error":
		if len(args) > 0 {
			return "Recorded simulated bridge error: " + strings.Join(args, " ") + "."
		}
		return "Recorded simulated bridge error."
	case "drop", "disconnect":
		return "Recorded simulated bridge connection drop."
	case "latency", "delay":
		if len(args) > 0 {
			return "Recorded simulated bridge latency " + args[0] + "."
		}
		return "Recorded simulated bridge latency."
	case "timeout":
		return "Recorded simulated bridge timeout."
	default:
		if len(args) > 0 {
			return "Recorded bridge diagnostic event " + action + " " + strings.Join(args, " ") + "."
		}
		return "Recorded bridge diagnostic event " + action + "."
	}
}
