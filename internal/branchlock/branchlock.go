package branchlock

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

type Intent struct {
	LaneID   string   `json:"lane_id"`
	Branch   string   `json:"branch"`
	Worktree string   `json:"worktree,omitempty"`
	Modules  []string `json:"modules,omitempty"`
}

type Collision struct {
	Branch  string   `json:"branch"`
	Module  string   `json:"module"`
	LaneIDs []string `json:"lane_ids"`
}

func (i *Intent) UnmarshalJSON(data []byte) error {
	type rawIntent struct {
		LaneID      string   `json:"lane_id"`
		LaneIDCamel string   `json:"laneId"`
		Branch      string   `json:"branch"`
		Worktree    string   `json:"worktree"`
		Modules     []string `json:"modules"`
	}
	var raw rawIntent
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	i.LaneID = firstNonEmpty(raw.LaneID, raw.LaneIDCamel)
	i.Branch = raw.Branch
	i.Worktree = raw.Worktree
	i.Modules = raw.Modules
	return nil
}

func Decode(data []byte) ([]Intent, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, nil
	}
	var intents []Intent
	if err := json.Unmarshal([]byte(text), &intents); err == nil {
		return NormalizeIntents(intents), nil
	}
	var envelope struct {
		Intents []Intent `json:"intents"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return nil, fmt.Errorf("branch lock input must be a JSON array or object with intents: %w", err)
	}
	if envelope.Intents == nil {
		return nil, fmt.Errorf("branch lock input object must include an intents array")
	}
	return NormalizeIntents(envelope.Intents), nil
}

func NormalizeIntents(intents []Intent) []Intent {
	out := make([]Intent, 0, len(intents))
	for index, intent := range intents {
		normalized := NormalizeIntent(intent, index)
		if normalized.Branch == "" {
			continue
		}
		out = append(out, normalized)
	}
	return out
}

func NormalizeIntent(intent Intent, index int) Intent {
	intent.LaneID = strings.TrimSpace(intent.LaneID)
	if intent.LaneID == "" {
		intent.LaneID = fmt.Sprintf("intent-%d", index+1)
	}
	intent.Branch = strings.TrimSpace(intent.Branch)
	intent.Worktree = strings.TrimSpace(intent.Worktree)
	modules := make([]string, 0, len(intent.Modules))
	seen := map[string]bool{}
	for _, module := range intent.Modules {
		module = normalizeModule(module)
		if module == "" || seen[module] {
			continue
		}
		seen[module] = true
		modules = append(modules, module)
	}
	sort.Strings(modules)
	intent.Modules = modules
	return intent
}

func DetectCollisions(intents []Intent) []Collision {
	intents = NormalizeIntents(intents)
	collisions := make([]Collision, 0)
	seen := map[string]bool{}
	for leftIndex, left := range intents {
		for _, right := range intents[leftIndex+1:] {
			if left.Branch == "" || left.Branch != right.Branch {
				continue
			}
			for _, module := range overlappingModules(left.Modules, right.Modules) {
				laneIDs := []string{left.LaneID, right.LaneID}
				key := left.Branch + "\x00" + module + "\x00" + strings.Join(laneIDs, "\x00")
				if seen[key] {
					continue
				}
				seen[key] = true
				collisions = append(collisions, Collision{
					Branch:  left.Branch,
					Module:  module,
					LaneIDs: laneIDs,
				})
			}
		}
	}
	sort.Slice(collisions, func(i, j int) bool {
		return collisions[i].Branch < collisions[j].Branch ||
			collisions[i].Branch == collisions[j].Branch && collisions[i].Module < collisions[j].Module ||
			collisions[i].Branch == collisions[j].Branch && collisions[i].Module == collisions[j].Module &&
				strings.Join(collisions[i].LaneIDs, "\x00") < strings.Join(collisions[j].LaneIDs, "\x00")
	})
	return collisions
}

func overlappingModules(left, right []string) []string {
	overlaps := make([]string, 0)
	seen := map[string]bool{}
	for _, leftModule := range left {
		for _, rightModule := range right {
			if !modulesOverlap(leftModule, rightModule) {
				continue
			}
			shared := sharedScope(leftModule, rightModule)
			if shared == "" || seen[shared] {
				continue
			}
			seen[shared] = true
			overlaps = append(overlaps, shared)
		}
	}
	sort.Strings(overlaps)
	return overlaps
}

func modulesOverlap(left, right string) bool {
	return left == right ||
		strings.HasPrefix(left, right+"/") ||
		strings.HasPrefix(right, left+"/")
}

func sharedScope(left, right string) string {
	if left == right || strings.HasPrefix(left, right+"/") {
		return right
	}
	return left
}

func normalizeModule(module string) string {
	module = strings.TrimSpace(strings.ReplaceAll(module, "\\", "/"))
	module = strings.TrimPrefix(module, "./")
	module = strings.Trim(module, "/")
	if module == "" || module == "." {
		return ""
	}
	module = path.Clean(module)
	if module == "." {
		return ""
	}
	return strings.Trim(module, "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
