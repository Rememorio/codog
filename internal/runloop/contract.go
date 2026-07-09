package runloop

import (
	"fmt"

	"github.com/Rememorio/codog/internal/anthropic"
)

// TranscriptIssue describes one Anthropic message transcript contract
// violation.
type TranscriptIssue struct {
	MessageIndex int    `json:"message_index"`
	BlockIndex   int    `json:"block_index,omitempty"`
	Code         string `json:"code"`
	Message      string `json:"message"`
}

// TranscriptReport summarizes structural compatibility checks for a model
// transcript.
type TranscriptReport struct {
	Valid           bool              `json:"valid"`
	MessageCount    int               `json:"message_count"`
	ToolUseCount    int               `json:"tool_use_count"`
	ToolResultCount int               `json:"tool_result_count"`
	Issues          []TranscriptIssue `json:"issues,omitempty"`
}

// ValidateTranscript checks the strict Claude-style tool pairing contract used
// by Codog's run loop: assistant tool_use blocks must have unique IDs, each
// must be answered by one user tool_result, and no assistant turn may appear
// while tool results are still pending.
func ValidateTranscript(messages []anthropic.Message) TranscriptReport {
	report := TranscriptReport{Valid: true, MessageCount: len(messages)}
	seenUses := map[string]TranscriptIssue{}
	pending := map[string]TranscriptIssue{}
	seenResults := map[string]TranscriptIssue{}

	for messageIndex, message := range messages {
		if message.Role == "assistant" && len(pending) > 0 {
			report.addIssue(messageIndex, 0, "pending_tool_results", "assistant message appeared before all pending tool results were returned")
		}
		if message.Role == "user" && len(pending) > 0 && !messageContainsOnlyToolResults(message) {
			report.addIssue(messageIndex, 0, "interleaved_user_content", "user content appeared before all pending tool results were returned")
		}
		for blockIndex, block := range message.Content {
			switch block.Type {
			case "tool_use":
				report.ToolUseCount++
				if message.Role != "assistant" {
					report.addIssue(messageIndex, blockIndex, "tool_use_role", "tool_use block must appear in an assistant message")
				}
				if block.ID == "" {
					report.addIssue(messageIndex, blockIndex, "tool_use_missing_id", "tool_use block is missing an id")
					continue
				}
				if block.Name == "" {
					report.addIssue(messageIndex, blockIndex, "tool_use_missing_name", fmt.Sprintf("tool_use %q is missing a tool name", block.ID))
				}
				if previous, ok := seenUses[block.ID]; ok {
					report.addIssue(messageIndex, blockIndex, "duplicate_tool_use_id", fmt.Sprintf("tool_use id %q was already used at message %d block %d", block.ID, previous.MessageIndex, previous.BlockIndex))
				}
				issue := TranscriptIssue{MessageIndex: messageIndex, BlockIndex: blockIndex}
				seenUses[block.ID] = issue
				pending[block.ID] = issue
			case "tool_result":
				report.ToolResultCount++
				if message.Role != "user" {
					report.addIssue(messageIndex, blockIndex, "tool_result_role", "tool_result block must appear in a user message")
				}
				if block.ToolUseID == "" {
					report.addIssue(messageIndex, blockIndex, "tool_result_missing_id", "tool_result block is missing a tool_use_id")
					continue
				}
				if _, ok := seenResults[block.ToolUseID]; ok {
					report.addIssue(messageIndex, blockIndex, "duplicate_tool_result", fmt.Sprintf("tool_use id %q already has a result", block.ToolUseID))
				}
				if _, ok := seenUses[block.ToolUseID]; !ok {
					report.addIssue(messageIndex, blockIndex, "orphan_tool_result", fmt.Sprintf("tool_result references unknown tool_use id %q", block.ToolUseID))
				}
				if _, ok := pending[block.ToolUseID]; !ok {
					report.addIssue(messageIndex, blockIndex, "unexpected_tool_result", fmt.Sprintf("tool_result references non-pending tool_use id %q", block.ToolUseID))
				}
				seenResults[block.ToolUseID] = TranscriptIssue{MessageIndex: messageIndex, BlockIndex: blockIndex}
				delete(pending, block.ToolUseID)
			}
		}
	}
	for id, issue := range pending {
		report.addIssue(issue.MessageIndex, issue.BlockIndex, "missing_tool_result", fmt.Sprintf("tool_use id %q has no tool_result", id))
	}
	report.Valid = len(report.Issues) == 0
	return report
}

// ValidateTurnResult checks the transcript contained in a completed run result.
func ValidateTurnResult(result TurnResult) error {
	report := ValidateTranscript(result.Messages)
	if report.Valid {
		return nil
	}
	return TranscriptContractError{Report: report}
}

// TranscriptContractError reports a malformed transcript produced by the run
// loop or supplied to a compatibility harness.
type TranscriptContractError struct {
	Report TranscriptReport
}

func (e TranscriptContractError) Error() string {
	if len(e.Report.Issues) == 0 {
		return "transcript contract violation"
	}
	first := e.Report.Issues[0]
	return fmt.Sprintf("transcript contract violation: %s at message %d block %d: %s", first.Code, first.MessageIndex, first.BlockIndex, first.Message)
}

func (r *TranscriptReport) addIssue(messageIndex int, blockIndex int, code string, message string) {
	r.Issues = append(r.Issues, TranscriptIssue{
		MessageIndex: messageIndex,
		BlockIndex:   blockIndex,
		Code:         code,
		Message:      message,
	})
}

func messageContainsOnlyToolResults(message anthropic.Message) bool {
	if len(message.Content) == 0 {
		return false
	}
	for _, block := range message.Content {
		if block.Type != "tool_result" {
			return false
		}
	}
	return true
}
