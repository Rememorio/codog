package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Rememorio/codog/internal/background"
	"github.com/Rememorio/codog/internal/gitops"
	"github.com/Rememorio/codog/internal/mockanthropic"
	"github.com/Rememorio/codog/internal/plugins"
	"github.com/Rememorio/codog/internal/runloop"
	"github.com/Rememorio/codog/internal/tools"
)

func gitPreserveStateScenario() scenario {
	return scenario{
		name: "git_preserve_state_roundtrip",
		runLocal: func(_ context.Context, workspace string) (localScenarioResult, error) {
			remote := filepath.Join(workspace, "origin.git")
			if err := runHarnessGit(workspace, "init", "-q", "--bare", remote); err != nil {
				return localScenarioResult{}, err
			}
			repo := filepath.Join(workspace, "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := runHarnessGit(repo, "init", "-q", "-b", "main"); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"config", "user.email", "codog@example.test"},
				{"config", "user.name", "Codog Test"},
			} {
				if err := runHarnessGit(repo, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			notesPath := filepath.Join(repo, "notes.txt")
			if err := os.WriteFile(notesPath, []byte("base\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"add", "notes.txt"},
				{"commit", "-q", "-m", "chore: base"},
				{"remote", "add", "origin", remote},
				{"push", "-q", "-u", "origin", "main"},
			} {
				if err := runHarnessGit(repo, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			baseSHA, err := gitops.Run(repo, "rev-parse", "HEAD")
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"add", "feature.txt"},
				{"commit", "-q", "-m", "feat: preserve state"},
			} {
				if err := runHarnessGit(repo, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			if err := os.WriteFile(notesPath, []byte("base\nworktree\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(repo, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}

			state, err := gitops.PreserveStateForIssue(repo)
			if err != nil {
				return localScenarioResult{}, err
			}
			if state == nil {
				return localScenarioResult{}, errors.New("git preserve state returned nil")
			}
			if state.RemoteBase != "origin/main" || state.RemoteBaseSHA != baseSHA || state.BranchName != "main" {
				return localScenarioResult{}, fmt.Errorf("unexpected preserved git identity: %#v", state)
			}
			for _, expected := range []string{"+feature", "+worktree"} {
				if !strings.Contains(state.Patch, expected) {
					return localScenarioResult{}, fmt.Errorf("preserved patch missing %s", expected)
				}
			}
			if !strings.Contains(state.FormatPatch, "feat: preserve state") {
				return localScenarioResult{}, fmt.Errorf("format patch missing commit subject")
			}
			if len(state.UntrackedFiles) != 1 || state.UntrackedFiles[0].Path != "scratch.txt" || state.UntrackedFiles[0].Content != "scratch\n" {
				return localScenarioResult{}, fmt.Errorf("unexpected untracked preservation: %#v", state.UntrackedFiles)
			}
			output, err := json.Marshal(map[string]any{
				"kind":              "git_preserve_state",
				"remote_base":       state.RemoteBase,
				"remote_base_sha":   state.RemoteBaseSHA,
				"branch_name":       state.BranchName,
				"patch_has_feature": strings.Contains(state.Patch, "+feature"),
				"patch_has_dirty":   strings.Contains(state.Patch, "+worktree"),
				"format_patch":      strings.TrimSpace(state.FormatPatch) != "",
				"share_sidecar":     true,
				"untracked_files":   len(state.UntrackedFiles),
			})
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(output),
				FinalMessage: "git preserve state harness ok",
				RequestCount: 1,
			}, nil
		},
	}
}

func worktreeLifecycleScenario() scenario {
	return scenario{
		name: "worktree_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			if err := runHarnessGit(workspace, "init", "-q", "-b", "main"); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"config", "user.email", "codog@example.test"},
				{"config", "user.name", "Codog Test"},
			} {
				if err := runHarnessGit(workspace, args...); err != nil {
					return localScenarioResult{}, err
				}
			}
			if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("worktree parity\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			for _, args := range [][]string{
				{"add", "README.md"},
				{"commit", "-q", "-m", "init worktree parity"},
			} {
				if err := runHarnessGit(workspace, args...); err != nil {
					return localScenarioResult{}, err
				}
			}

			registry := tools.NewRegistry(workspace)
			enterOut, err := registry.Execute(ctx, "EnterWorktreeTool", json.RawMessage(`{"name":"reviewer"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var entered struct {
				Kind       string `json:"kind"`
				Operation  string `json:"operation"`
				Allocation struct {
					ID   string `json:"id"`
					Path string `json:"path"`
					Ref  string `json:"ref"`
				} `json:"allocation"`
			}
			if err := json.Unmarshal([]byte(enterOut), &entered); err != nil {
				return localScenarioResult{}, err
			}
			if entered.Kind != "worktree" || entered.Operation != "enter" || entered.Allocation.ID == "" || entered.Allocation.Path == "" || entered.Allocation.Ref == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected enter worktree output: %s", enterOut)
			}
			removed := false
			defer func() {
				if !removed {
					_, _ = registry.Execute(ctx, "ExitWorktreeTool", json.RawMessage(fmt.Sprintf(`{"id":%q}`, entered.Allocation.ID)), nil)
				}
			}()
			checkoutReadme := filepath.Join(entered.Allocation.Path, "README.md")
			data, err := os.ReadFile(checkoutReadme)
			if err != nil {
				return localScenarioResult{}, err
			}
			if string(data) != "worktree parity\n" {
				return localScenarioResult{}, fmt.Errorf("unexpected checkout README content: %q", string(data))
			}
			metadataPath := filepath.Join(workspace, ".codog", "worktrees", "metadata", entered.Allocation.ID+".json")
			if _, err := os.Stat(metadataPath); err != nil {
				return localScenarioResult{}, fmt.Errorf("missing worktree metadata: %w", err)
			}

			exitOut, err := registry.Execute(ctx, "exit_worktree", json.RawMessage(fmt.Sprintf(`{"id":%q}`, entered.Allocation.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var exited struct {
				Kind      string `json:"kind"`
				Operation string `json:"operation"`
				ID        string `json:"id"`
				Removed   bool   `json:"removed"`
			}
			if err := json.Unmarshal([]byte(exitOut), &exited); err != nil {
				return localScenarioResult{}, err
			}
			if exited.Kind != "worktree" || exited.Operation != "exit" || exited.ID != entered.Allocation.ID || !exited.Removed {
				return localScenarioResult{}, fmt.Errorf("unexpected exit worktree output: %s", exitOut)
			}
			removed = true
			if _, err := os.Stat(entered.Allocation.Path); !os.IsNotExist(err) {
				return localScenarioResult{}, fmt.Errorf("worktree path still exists or stat failed: %v", err)
			}
			if _, err := os.Stat(metadataPath); !os.IsNotExist(err) {
				return localScenarioResult{}, fmt.Errorf("worktree metadata still exists or stat failed: %v", err)
			}
			return localScenarioResult{
				Output:       strings.Join([]string{enterOut, exitOut, "worktree lifecycle harness ok"}, "\n"),
				FinalMessage: "worktree lifecycle harness ok",
				ToolCalls:    2,
				ToolUses:     []string{"enter_worktree", "exit_worktree"},
				RequestCount: 2,
			}, nil
		},
	}
}

func runHarnessGit(workspace string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func planTodoScenario() scenario {
	return scenario{
		name: "plan_todo_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			enterOut, err := tools.EnterPlanModeTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"plan":"1. Inspect workspace\n2. Update tests"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "plan"`, `"action": "enter"`, `"status": "active"`, "Inspect workspace"} {
				if !strings.Contains(enterOut, expected) {
					return localScenarioResult{}, fmt.Errorf("plan enter output missing %s", expected)
				}
			}

			editorLog := filepath.Join(workspace, "plan-editor.log")
			editorScript := filepath.Join(workspace, "plan-editor.sh")
			if err := os.WriteFile(editorScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" > \"$1\"\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			openOut, err := runHarnessCodogWithEnv(ctx, workspace, []string{"VISUAL=" + editorScript + " " + editorLog}, "plan", "open", "--output-format", "json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var opened struct {
				Action string `json:"action"`
				Status string `json:"status"`
				Opened bool   `json:"opened"`
			}
			if err := json.Unmarshal([]byte(openOut), &opened); err != nil {
				return localScenarioResult{}, fmt.Errorf("plan open output was not json: %w: %s", err, openOut)
			}
			if opened.Action != "open" || opened.Status != "opened" || !opened.Opened {
				return localScenarioResult{}, fmt.Errorf("unexpected plan open output: %s", openOut)
			}
			openedPath, err := os.ReadFile(editorLog)
			if err != nil {
				return localScenarioResult{}, err
			}
			actualPlanPath, err := filepath.EvalSymlinks(strings.TrimSpace(string(openedPath)))
			if err != nil {
				return localScenarioResult{}, err
			}
			expectedPlanPath, err := filepath.EvalSymlinks(filepath.Join(workspace, ".codog", "plan.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if actualPlanPath != expectedPlanPath {
				return localScenarioResult{}, fmt.Errorf("plan editor opened unexpected path: %s", string(openedPath))
			}

			writeOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "write focused parity test",
						"activeForm": "writing focused parity test",
						"status": "in_progress",
						"priority": "high"
					},
					{
						"content": "run smoke",
						"activeForm": "running smoke",
						"status": "pending",
						"priority": "medium"
					}
				]
			}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 2`, `"content": "write focused parity test"`, `"status": "in_progress"`, `"newTodos": [`} {
				if !strings.Contains(writeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("todo write output missing %s", expected)
				}
			}

			readOut, err := tools.TodoReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "list"`, `"total": 2`, `"content": "run smoke"`} {
				if !strings.Contains(readOut, expected) {
					return localScenarioResult{}, fmt.Errorf("todo read output missing %s", expected)
				}
			}

			exitOut, err := tools.ExitPlanModeTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{"plan":"Final plan: implement, test, smoke"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "plan"`, `"action": "exit"`, `"status": "inactive"`, "Final plan: implement, test, smoke"} {
				if !strings.Contains(exitOut, expected) {
					return localScenarioResult{}, fmt.Errorf("plan exit output missing %s", expected)
				}
			}

			planData, err := os.ReadFile(filepath.Join(workspace, ".codog", "plan.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(planData), `"active": false`) || !strings.Contains(string(planData), "Final plan: implement, test, smoke") {
				return localScenarioResult{}, fmt.Errorf("persisted plan state was not finalized: %s", string(planData))
			}
			todoData, err := os.ReadFile(filepath.Join(workspace, ".codog", "todos.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(string(todoData), `"kind": "todos"`) || !strings.Contains(string(todoData), "write focused parity test") {
				return localScenarioResult{}, fmt.Errorf("persisted todo state missing active items: %s", string(todoData))
			}

			return localScenarioResult{
				Output:       strings.Join([]string{enterOut, openOut, writeOut, readOut, exitOut}, "\n"),
				FinalMessage: "plan todo harness ok",
				ToolCalls:    4,
				ToolUses:     []string{"enter_plan_mode", "todo_write", "todo_read", "exit_plan_mode"},
				RequestCount: 5,
			}, nil
		},
	}
}

func todoCompletionVerificationScenario() scenario {
	return scenario{
		name: "todo_completion_verification_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			initialOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "draft implementation",
						"activeForm": "drafting implementation",
						"status": "in_progress",
						"priority": "high"
					}
				]
			}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 1`, `"content": "draft implementation"`} {
				if !strings.Contains(initialOut, expected) {
					return localScenarioResult{}, fmt.Errorf("initial todo write output missing %s", expected)
				}
			}

			completedOut, err := tools.TodoWriteTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{
				"todos": [
					{
						"content": "draft implementation",
						"activeForm": "drafting implementation",
						"status": "completed",
						"priority": "high"
					},
					{
						"content": "update tests",
						"activeForm": "updating tests",
						"status": "completed",
						"priority": "medium"
					},
					{
						"content": "prepare summary",
						"activeForm": "preparing summary",
						"status": "completed",
						"priority": "low"
					}
				]
			}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "replace"`, `"total": 0`, `"oldTodos": [`, `"content": "draft implementation"`, `"verificationNudgeNeeded": true`} {
				if !strings.Contains(completedOut, expected) {
					return localScenarioResult{}, fmt.Errorf("completed todo write output missing %s", expected)
				}
			}

			readOut, err := tools.TodoReadTool{Workspace: workspace}.Execute(ctx, json.RawMessage(`{}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"kind": "todos"`, `"action": "list"`, `"total": 0`} {
				if !strings.Contains(readOut, expected) {
					return localScenarioResult{}, fmt.Errorf("todo read output missing %s", expected)
				}
			}
			if strings.Contains(readOut, "draft implementation") || strings.Contains(readOut, "update tests") {
				return localScenarioResult{}, fmt.Errorf("completed todos were not cleared from read output: %s", readOut)
			}

			todoData, err := os.ReadFile(filepath.Join(workspace, ".codog", "todos.json"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if strings.Contains(string(todoData), "draft implementation") || strings.Contains(string(todoData), "update tests") {
				return localScenarioResult{}, fmt.Errorf("completed todos were not cleared from persisted state: %s", string(todoData))
			}

			return localScenarioResult{
				Output:       strings.Join([]string{initialOut, completedOut, readOut}, "\n"),
				FinalMessage: "todo completion verification harness ok",
				ToolCalls:    3,
				ToolUses:     []string{"todo_write", "todo_write", "todo_read"},
				RequestCount: 3,
			}, nil
		},
	}
}

func lspStaticScenario() scenario {
	return scenario{
		name: "lsp_static_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			pkgDir := filepath.Join(workspace, "pkg")
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module example.test/harness\n\ngo 1.25\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			source := "package pkg\n\ntype Runner struct{}\n\nfunc RunFast() Runner { return Runner{} }\n\nfunc UseRunner() Runner { return RunFast() }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "runner.go"), []byte(source), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			messy := "package pkg\n\nfunc messy(){println(\"hi\")}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "messy.go"), []byte(messy), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			broken := "package pkg\n\nfunc Broken() { MissingSymbol() }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "broken.go"), []byte(broken), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			foldSource := "package pkg\n\nfunc FoldOnly() {\n\tprintln(\"fold\")\n}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "fold.go"), []byte(foldSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			linkSource := "package pkg\n\n// Docs: https://example.test/docs.\nconst Link = \"https://example.test/api\"\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "links.go"), []byte(linkSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			colorSource := "package pkg\n\nconst Accent = \"#336699\"\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "colors.go"), []byte(colorSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			hintSource := "package pkg\n\nfunc Build(name string, count int) int { return count }\nfunc UseBuild() { _ = Build(\"codog\", 2) }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "hints.go"), []byte(hintSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			hierarchySource := "package pkg\n\ntype TypeBase struct{}\ntype TypeChild struct{ TypeBase }\ntype TypeContract interface { Build() }\nfunc (TypeChild) Build() {}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "hierarchy.go"), []byte(hierarchySource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			inlineSource := "package pkg\n\nconst InlineAnswer = 42\n\nfunc InlineValuesDemo() {\n\tlocal := \"codog\"\n\t_ = local\n}\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "inline.go"), []byte(inlineSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			importsSource := "package pkg\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n\t\"bytes\"\n\t\"fmt\"\n)\n\nfunc ImportsDemo(){ fmt.Println(strings.TrimSpace(\" hi \")) }\n"
			if err := os.WriteFile(filepath.Join(pkgDir, "imports.go"), []byte(importsSource), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			hintArgChar := strings.Index(strings.Split(hintSource, "\n")[3], `"codog"`)
			tool := tools.LSPTool{Workspace: workspace}

			symbolsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"symbols","path":"pkg/runner.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "symbols"`, `"source": "static"`, `"name": "Runner"`, `"name": "RunFast"`, `"total": 3`} {
				if !strings.Contains(symbolsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp symbols output missing %s", expected)
				}
			}

			workspaceSymbolsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"workspace_symbol","query":"run","limit":3}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "workspace-symbol"`, `"source": "static"`, `"query": "run"`, `"name": "Runner"`, `"name": "RunFast"`, `"total": 3`} {
				if !strings.Contains(workspaceSymbolsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp workspace symbols output missing %s", expected)
				}
			}

			workspaceSymbolResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"workspace_symbol_resolve","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "workspace-symbol-resolve"`, `"source": "static"`, `"found": true`, `"name": "RunFast"`, `"symbol": "RunFast"`, `"snippet": [`} {
				if !strings.Contains(workspaceSymbolResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp workspace symbol resolve output missing %s", expected)
				}
			}

			definitionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"definition","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "definition"`, `"found": true`, `"name": "RunFast"`, `"path": "pkg/runner.go"`} {
				if !strings.Contains(definitionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp definition output missing %s", expected)
				}
			}

			declarationOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"declaration","query":"Runner"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "declaration"`, `"source": "static"`, `"found": true`, `"name": "Runner"`, `"path": "pkg/runner.go"`} {
				if !strings.Contains(declarationOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp declaration output missing %s", expected)
				}
			}

			typeDefinitionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"type_definition","query":"Runner"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "type-definition"`, `"source": "static"`, `"found": true`, `"name": "Runner"`, `"path": "pkg/runner.go"`} {
				if !strings.Contains(typeDefinitionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp type definition output missing %s", expected)
				}
			}

			documentHighlightOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_highlight","path":"pkg/runner.go","query":"Runner","limit":3}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-highlight"`, `"source": "static"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"character": 5`, `"total": 3`} {
				if !strings.Contains(documentHighlightOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document highlight output missing %s", expected)
				}
			}

			foldingRangeOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"folding_range","path":"pkg/fold.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "folding-range"`, `"source": "static"`, `"path": "pkg/fold.go"`, `"startLine": 2`, `"endLine": 4`, `"total": 1`} {
				if !strings.Contains(foldingRangeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp folding range output missing %s", expected)
				}
			}

			selectionRangeOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"selection_range","path":"pkg/runner.go","line":4,"character":6,"limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "selection-range"`, `"source": "static"`, `"path": "pkg/runner.go"`, `"kind": "Ident"`, `"character": 5`} {
				if !strings.Contains(selectionRangeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp selection range output missing %s", expected)
				}
			}

			monikerOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"moniker","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "moniker"`, `"source": "static"`, `"scheme": "gomod"`, `"identifier": "example.test/harness/pkg.RunFast"`, `"kind": "export"`, `"unique": "project"`} {
				if !strings.Contains(monikerOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp moniker output missing %s", expected)
				}
			}

			linkedEditingOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"linked_editing_range","path":"pkg/runner.go","query":"Runner","limit":3}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "linked-editing-range"`, `"source": "static"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"wordPattern": "[A-Za-z_][A-Za-z0-9_]*"`, `"total": 3`} {
				if !strings.Contains(linkedEditingOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp linked editing output missing %s", expected)
				}
			}

			documentLinkOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_link","path":"pkg/links.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-link"`, `"source": "static"`, `"path": "pkg/links.go"`, `"target": "https://example.test/docs"`, `"character": 9`, `"total": 2`} {
				if !strings.Contains(documentLinkOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document link output missing %s", expected)
				}
			}

			documentLinkResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_link_resolve","path":"pkg/links.go","line":2,"character":12}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-link-resolve"`, `"source": "static"`, `"found": true`, `"target": "https://example.test/docs"`} {
				if !strings.Contains(documentLinkResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document link resolve output missing %s", expected)
				}
			}

			documentColorOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_color","path":"pkg/colors.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-color"`, `"source": "static"`, `"path": "pkg/colors.go"`, `"text": "#336699"`, `"red": 0.2`, `"total": 1`} {
				if !strings.Contains(documentColorOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document color output missing %s", expected)
				}
			}

			colorPresentationOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"color_presentation","path":"pkg/colors.go","line":2,"character":18}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "color-presentation"`, `"source": "static"`, `"found": true`, `"label": "#336699"`, `"label": "rgb(51, 102, 153)"`} {
				if !strings.Contains(colorPresentationOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp color presentation output missing %s", expected)
				}
			}

			inlayHintOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"inlay_hint","path":"pkg/hints.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "inlay-hint"`, `"source": "static"`, `"path": "pkg/hints.go"`, `"label": "name:"`, `"label": "count:"`, `"kind": "parameter"`, `"total": 2`} {
				if !strings.Contains(inlayHintOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp inlay hint output missing %s", expected)
				}
			}

			inlayHintResolveOut, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"action":"inlay_hint_resolve","path":"pkg/hints.go","line":3,"character":%d}`, hintArgChar)))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "inlay-hint-resolve"`, `"source": "static"`, `"found": true`, `"label": "name:"`, `"tooltip": "Build parameter 1"`} {
				if !strings.Contains(inlayHintResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp inlay hint resolve output missing %s", expected)
				}
			}

			signatureHelpOut, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"action":"signature_help","path":"pkg/hints.go","line":3,"character":%d}`, hintArgChar)))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "signature-help"`, `"source": "static"`, `"found": true`, `"function": "Build"`, `"label": "Build(name string, count int) int"`, `"activeParameter": 0`} {
				if !strings.Contains(signatureHelpOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp signature help output missing %s", expected)
				}
			}

			codeLensOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_lens","path":"pkg/runner.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-lens"`, `"source": "static"`, `"path": "pkg/runner.go"`, `"symbol": "Runner"`, `"command": "codog.references"`, `"total": 3`} {
				if !strings.Contains(codeLensOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code lens output missing %s", expected)
				}
			}

			codeLensResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_lens_resolve","path":"pkg/runner.go","line":2,"character":6}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-lens-resolve"`, `"source": "static"`, `"found": true`, `"symbol": "Runner"`, `"command": "codog.references"`} {
				if !strings.Contains(codeLensResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code lens resolve output missing %s", expected)
				}
			}

			semanticTokensOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"semantic_tokens","path":"pkg/runner.go","limit":80}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "semantic-tokens"`, `"source": "static"`, `"legend": [`, `"text": "Runner"`, `"type": "type"`, `"text": "RunFast"`, `"type": "function"`} {
				if !strings.Contains(semanticTokensOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp semantic tokens output missing %s", expected)
				}
			}

			semanticTokensRangeOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"semantic_tokens_range","path":"pkg/runner.go","line":2,"limit":20}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "semantic-tokens-range"`, `"source": "static"`, `"text": "Runner"`, `"line": 2`} {
				if !strings.Contains(semanticTokensRangeOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp semantic tokens range output missing %s", expected)
				}
			}

			semanticTokensDeltaOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"semantic_tokens_delta","path":"pkg/runner.go","query":"previous-result","limit":80}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "semantic-tokens-delta"`, `"source": "static"`, `"previousResultId": "previous-result"`, `"edits": []`} {
				if !strings.Contains(semanticTokensDeltaOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp semantic tokens delta output missing %s", expected)
				}
			}

			prepareRenameOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"prepare_rename","path":"pkg/runner.go","line":2,"character":6}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "prepare-rename"`, `"source": "static"`, `"found": true`, `"symbol": "Runner"`, `"placeholder": "Runner"`} {
				if !strings.Contains(prepareRenameOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp prepare rename output missing %s", expected)
				}
			}

			renameOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"rename","query":"Runner","new_name":"RunnerRenamed","limit":20}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "rename"`, `"source": "static"`, `"query": "Runner"`, `"newName": "RunnerRenamed"`, `"file_edits": 1`, "type RunnerRenamed struct{}"} {
				if !strings.Contains(renameOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp rename output missing %s", expected)
				}
			}

			callHierarchyOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"prepare_call_hierarchy","query":"Build"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "prepare-call-hierarchy"`, `"source": "static"`, `"name": "Build"`, `"kind": "function"`, `"total": 1`} {
				if !strings.Contains(callHierarchyOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp prepare call hierarchy output missing %s", expected)
				}
			}

			incomingCallsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"incoming_calls","query":"Build","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "call-hierarchy-incoming"`, `"source": "static"`, `"query": "Build"`, `"name": "UseBuild"`, `"name": "Build"`, `"total": 1`} {
				if !strings.Contains(incomingCallsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp incoming calls output missing %s", expected)
				}
			}

			outgoingCallsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"outgoing_calls","query":"UseBuild","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "call-hierarchy-outgoing"`, `"source": "static"`, `"query": "UseBuild"`, `"name": "Build"`, `"total": 1`} {
				if !strings.Contains(outgoingCallsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp outgoing calls output missing %s", expected)
				}
			}

			typeHierarchyOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"prepare_type_hierarchy","query":"TypeBase"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "prepare-type-hierarchy"`, `"source": "static"`, `"name": "TypeBase"`, `"kind": "struct"`, `"total": 1`} {
				if !strings.Contains(typeHierarchyOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp prepare type hierarchy output missing %s", expected)
				}
			}

			typeHierarchySupertypesOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"supertypes","query":"TypeChild","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "type-hierarchy-supertypes"`, `"source": "static"`, `"query": "TypeChild"`, `"name": "TypeBase"`, `"total": 1`} {
				if !strings.Contains(typeHierarchySupertypesOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp type hierarchy supertypes output missing %s", expected)
				}
			}

			typeHierarchySubtypesOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"subtypes","query":"TypeBase","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "type-hierarchy-subtypes"`, `"source": "static"`, `"query": "TypeBase"`, `"name": "TypeChild"`, `"total": 1`} {
				if !strings.Contains(typeHierarchySubtypesOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp type hierarchy subtypes output missing %s", expected)
				}
			}

			implementationOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"implementation","query":"TypeContract","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "implementation"`, `"source": "static"`, `"query": "TypeContract"`, `"name": "TypeChild"`, `"total": 1`} {
				if !strings.Contains(implementationOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp implementation output missing %s", expected)
				}
			}

			referencesOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"references","query":"Runner","limit":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "references"`, `"query": "Runner"`, `"path": "pkg/runner.go"`, `"total": 3`} {
				if !strings.Contains(referencesOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp references output missing %s", expected)
				}
			}

			hoverOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"hover","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "hover"`, `"found": true`, `"kind": "function"`, `"symbol": "RunFast"`} {
				if !strings.Contains(hoverOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp hover output missing %s", expected)
				}
			}

			completionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"completion","query":"Run","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "completion"`, `"label": "RunFast"`, `"kind": "function"`} {
				if !strings.Contains(completionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp completion output missing %s", expected)
				}
			}

			completionResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"completion_resolve","query":"RunFast"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "completion-item-resolve"`, `"source": "static"`, `"found": true`, `"label": "RunFast"`, `"kind": "function"`} {
				if !strings.Contains(completionResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp completion resolve output missing %s", expected)
				}
			}

			rangeFormatOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"range_format","path":"pkg/messy.go","line":2,"character":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "range-format"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"changed": true`, "func messy()"} {
				if !strings.Contains(rangeFormatOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp range format output missing %s", expected)
				}
			}

			onTypeFormatOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"on_type_format","path":"pkg/messy.go","line":2,"character":18}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "on-type-format"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"changed": true`} {
				if !strings.Contains(onTypeFormatOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp on type format output missing %s", expected)
				}
			}

			willSaveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"will_save","path":"pkg/messy.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "will-save"`, `"source": "static"`, `"path": "pkg/messy.go"`, `"edits": true`} {
				if !strings.Contains(willSaveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp will save output missing %s", expected)
				}
			}

			codeActionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action","path":"pkg/messy.go","line":2,"character":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action"`, `"source": "static"`, `"title": "Format Go file"`, `"kind": "source.format"`, `"title": "Fix all Go source"`, `"kind": "source.fixAll"`, `"total": 2`} {
				if !strings.Contains(codeActionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code action output missing %s", expected)
				}
			}

			organizeActionOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action","path":"pkg/imports.go","line":2,"character":10}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action"`, `"source": "static"`, `"title": "Organize Go imports"`, `"kind": "source.organizeImports"`, `"removed_imports": [`, `"bytes"`, `"duplicate_imports": [`, `"fmt"`} {
				if !strings.Contains(organizeActionOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp organize imports action output missing %s", expected)
				}
			}

			codeActionResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action_resolve","path":"pkg/messy.go","query":"Format Go file"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "Format Go file"`, `"title": "Format Go file"`, "func messy()"} {
				if !strings.Contains(codeActionResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp code action resolve output missing %s", expected)
				}
			}

			organizeResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action_resolve","path":"pkg/imports.go","query":"source.organizeImports"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "source.organizeImports"`, `"title": "Organize Go imports"`, `"kind": "organize_imports"`, `"removed_imports": [`} {
				if !strings.Contains(organizeResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp organize imports resolve output missing %s", expected)
				}
			}

			fixAllResolveOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"code_action_resolve","path":"pkg/imports.go","query":"source.fixAll"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "code-action-resolve"`, `"source": "static"`, `"selected": "source.fixAll"`, `"title": "Fix all Go source"`, `"kind": "fix_all"`, `"source.organizeImports"`} {
				if !strings.Contains(fixAllResolveOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp fix all resolve output missing %s", expected)
				}
			}

			inlineValueOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"inline_value","path":"pkg/inline.go","limit":5}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "inline-value"`, `"source": "static"`, `"name": "InlineAnswer"`, `"text": "local = \"codog\""`, `"total": 2`} {
				if !strings.Contains(inlineValueOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp inline value output missing %s", expected)
				}
			}

			executeCommandOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"execute_command","query":"format","path":"pkg/messy.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "execute-command"`, `"source": "static"`, `"command": "format"`, `"path": "pkg/messy.go"`, "func messy()"} {
				if !strings.Contains(executeCommandOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp execute command output missing %s", expected)
				}
			}

			organizeCommandOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"execute_command","query":"source.organizeImports","path":"pkg/imports.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "execute-command"`, `"source": "static"`, `"command": "source.organizeimports"`, `"path": "pkg/imports.go"`, `"organize_imports": {`, `"removed_imports": [`} {
				if !strings.Contains(organizeCommandOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp organize imports execute command output missing %s", expected)
				}
			}

			fixAllCommandOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"execute_command","query":"source.fixAll","path":"pkg/imports.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "execute-command"`, `"source": "static"`, `"command": "source.fixall"`, `"path": "pkg/imports.go"`, `"fix_all": {`, `"kind": "fix_all"`, `"source.organizeImports"`} {
				if !strings.Contains(fixAllCommandOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp fix all execute command output missing %s", expected)
				}
			}

			documentDiagnosticOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"document_diagnostic","path":"pkg/broken.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "document-diagnostic"`, `"source": "static"`, `"path": "pkg/broken.go"`, `"total": 2`, "MissingSymbol"} {
				if !strings.Contains(documentDiagnosticOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp document diagnostic output missing %s", expected)
				}
			}

			workspaceDiagnosticOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"workspace_diagnostic"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "workspace-diagnostic"`, `"source": "static"`, `"path": "pkg/broken.go"`, "MissingSymbol"} {
				if !strings.Contains(workspaceDiagnosticOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp workspace diagnostic output missing %s", expected)
				}
			}

			diagnosticsOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"diagnostics"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "diagnostics"`, `"path": "pkg/broken.go"`, `"line": 3`, "MissingSymbol"} {
				if !strings.Contains(diagnosticsOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp diagnostics output missing %s", expected)
				}
			}

			formatOut, err := tool.Execute(ctx, json.RawMessage(`{"action":"format","path":"pkg/messy.go"}`))
			if err != nil {
				return localScenarioResult{}, err
			}
			for _, expected := range []string{`"action": "format"`, `"kind": "format"`, `"path": "pkg/messy.go"`, `"changed": true`, "func messy()"} {
				if !strings.Contains(formatOut, expected) {
					return localScenarioResult{}, fmt.Errorf("lsp format output missing %s", expected)
				}
			}
			data, err := os.ReadFile(filepath.Join(pkgDir, "messy.go"))
			if err != nil {
				return localScenarioResult{}, err
			}
			if string(data) != messy {
				return localScenarioResult{}, fmt.Errorf("lsp format unexpectedly modified file")
			}

			toolUses := make([]string, 52)
			for i := range toolUses {
				toolUses[i] = "lsp"
			}
			return localScenarioResult{
				Output:       strings.Join([]string{symbolsOut, workspaceSymbolsOut, workspaceSymbolResolveOut, definitionOut, declarationOut, typeDefinitionOut, documentHighlightOut, foldingRangeOut, selectionRangeOut, monikerOut, linkedEditingOut, documentLinkOut, documentLinkResolveOut, documentColorOut, colorPresentationOut, inlayHintOut, inlayHintResolveOut, signatureHelpOut, codeLensOut, codeLensResolveOut, semanticTokensOut, semanticTokensRangeOut, semanticTokensDeltaOut, prepareRenameOut, renameOut, callHierarchyOut, incomingCallsOut, outgoingCallsOut, typeHierarchyOut, typeHierarchySupertypesOut, typeHierarchySubtypesOut, implementationOut, referencesOut, hoverOut, completionOut, completionResolveOut, rangeFormatOut, onTypeFormatOut, willSaveOut, codeActionOut, organizeActionOut, codeActionResolveOut, organizeResolveOut, fixAllResolveOut, inlineValueOut, executeCommandOut, organizeCommandOut, fixAllCommandOut, documentDiagnosticOut, workspaceDiagnosticOut, diagnosticsOut, formatOut}, "\n"),
				FinalMessage: "lsp static harness ok",
				ToolCalls:    52,
				ToolUses:     toolUses,
				RequestCount: 52,
			}, nil
		},
	}
}

func lspCLIMetadataScenario() scenario {
	return scenario{
		name: "lsp_cli_metadata_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			if err := os.MkdirAll(configHome, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			configPath := filepath.Join(workspace, "codog-config.json")
			configData, err := json.Marshal(map[string]any{"config_home": configHome})
			if err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(configPath, configData, 0o644); err != nil {
				return localScenarioResult{}, err
			}

			actionsOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "actions", "--json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var actions lspActionsHarnessReport
			if err := json.Unmarshal([]byte(actionsOut), &actions); err != nil {
				return localScenarioResult{}, err
			}
			if actions.Kind != "lsp_actions" || actions.Action != "actions" || actions.Status != "ok" || actions.Count < 40 {
				return localScenarioResult{}, fmt.Errorf("unexpected lsp actions report: %#v", actions)
			}
			if !strings.Contains(actionsOut, `"name": "definition"`) || !strings.Contains(actionsOut, `"method": "textDocument/definition"`) || !strings.Contains(actionsOut, `"name": "references"`) {
				return localScenarioResult{}, fmt.Errorf("lsp actions output missing expected actions: %s", actionsOut)
			}

			discoverOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "discover", "--json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var discover lspDiscoverHarnessReport
			if err := json.Unmarshal([]byte(discoverOut), &discover); err != nil {
				return localScenarioResult{}, err
			}
			if discover.Kind != "lsp_discover" || discover.Action != "discover" || discover.Status != "ok" || discover.Count < 5 {
				return localScenarioResult{}, fmt.Errorf("unexpected lsp discover report: %#v", discover)
			}
			if !lspHarnessCandidateExists(discover.Candidates, "go", "gopls") || !lspHarnessCandidateExists(discover.Candidates, "rust", "rust-analyzer") {
				return localScenarioResult{}, fmt.Errorf("lsp discover candidates missing expected defaults: %#v", discover.Candidates)
			}

			listOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "list", "--json")
			if err != nil {
				return localScenarioResult{}, err
			}
			var list lspListHarnessReport
			if err := json.Unmarshal([]byte(listOut), &list); err != nil {
				return localScenarioResult{}, err
			}
			if list.Kind != "lsp_list" || list.Action != "list" || list.Status != "ok" || list.Count != 0 || len(list.Servers) != 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected lsp list report: %#v", list)
			}

			textOut, err := runHarnessCodog(ctx, workspace, "--config", configPath, "code-intel", "lsp", "discover", "--output-format", "text")
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(textOut, "LSP Discover") || !strings.Contains(textOut, "gopls") || !strings.Contains(textOut, "rust-analyzer") {
				return localScenarioResult{}, fmt.Errorf("lsp discover text output missing expected values: %s", textOut)
			}

			report := map[string]any{
				"kind": "lsp_cli_metadata",
				"lsp": map[string]any{
					"actions":       actions.Count,
					"candidates":    discover.Count,
					"servers":       list.Count,
					"has_go":        lspHarnessCandidateExists(discover.Candidates, "go", "gopls"),
					"has_rust":      lspHarnessCandidateExists(discover.Candidates, "rust", "rust-analyzer"),
					"text_rendered": strings.Contains(textOut, "LSP Discover"),
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "lsp cli metadata harness ok",
				RequestCount: 4,
				MessageCount: 1,
			}, nil
		},
	}
}

type lspActionsHarnessReport struct {
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type lspDiscoverHarnessReport struct {
	Kind       string                `json:"kind"`
	Action     string                `json:"action"`
	Status     string                `json:"status"`
	Count      int                   `json:"count"`
	Candidates []lspHarnessCandidate `json:"candidates"`
}

type lspListHarnessReport struct {
	Kind    string            `json:"kind"`
	Action  string            `json:"action"`
	Status  string            `json:"status"`
	Count   int               `json:"count"`
	Servers []json.RawMessage `json:"servers"`
}

type lspHarnessCandidate struct {
	Language string `json:"language"`
	Command  string `json:"command"`
}

func lspHarnessCandidateExists(candidates []lspHarnessCandidate, language string, command string) bool {
	for _, candidate := range candidates {
		if candidate.Language == language && candidate.Command == command {
			return true
		}
	}
	return false
}

func pluginLifecycleScenario() scenario {
	var installedRoot string
	var disabledRoot string
	return scenario{
		name:   "plugin_lifecycle_roundtrip",
		turns:  []mockanthropic.Turn{{Text: "plugin lifecycle harness ok"}},
		prompt: "verify plugin lifecycle",
		setup: func(workspace string) error {
			source := filepath.Join(workspace, "plugin-source")
			if err := os.MkdirAll(source, 0o755); err != nil {
				return err
			}
			manifest := `{"id":"lifecycle","name":"lifecycle","version":"1.0.0","description":"Lifecycle harness plugin","lifecycle":{"init":["echo init-ok > lifecycle-init.txt"],"shutdown":["echo shutdown-ok > lifecycle-shutdown.txt"]},"tools":[{"name":"lifecycle_tool","command":"cat","permission":"read-only"}]}`
			if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(source, "tool.sh"), []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
				return err
			}
			installed, err := plugins.Install(workspace, source)
			if err != nil {
				return err
			}
			installedRoot = installed.Root
			if !installed.Enabled {
				return fmt.Errorf("installed plugin is disabled")
			}
			initRun := plugins.RunLifecycle(context.Background(), installed, "init", 5*time.Second)
			if initRun.Status != "ok" {
				return fmt.Errorf("init lifecycle failed: %s", initRun.Message)
			}
			initMarker, err := os.ReadFile(filepath.Join(installed.Root, "lifecycle-init.txt"))
			if err != nil {
				return err
			}
			if !strings.Contains(string(initMarker), "init-ok") {
				return fmt.Errorf("init lifecycle marker mismatch: %q", string(initMarker))
			}
			shutdownRun := plugins.RunLifecycle(context.Background(), installed, "shutdown", 5*time.Second)
			if shutdownRun.Status != "ok" {
				return fmt.Errorf("shutdown lifecycle failed: %s", shutdownRun.Message)
			}
			shutdownMarker, err := os.ReadFile(filepath.Join(installed.Root, "lifecycle-shutdown.txt"))
			if err != nil {
				return err
			}
			if !strings.Contains(string(shutdownMarker), "shutdown-ok") {
				return fmt.Errorf("shutdown lifecycle marker mismatch: %q", string(shutdownMarker))
			}
			disabled, err := plugins.Disable(workspace, installed.ID)
			if err != nil {
				return err
			}
			disabledRoot = disabled.Root
			if disabled.Enabled {
				return fmt.Errorf("disabled plugin still reports enabled")
			}
			if _, err := os.Stat(filepath.Join(disabled.Root, plugins.DisabledMarker)); err != nil {
				return err
			}
			enabled, err := plugins.Enable(workspace, installed.ID)
			if err != nil {
				return err
			}
			if !enabled.Enabled {
				return fmt.Errorf("enabled plugin still reports disabled")
			}
			if _, err := os.Stat(filepath.Join(enabled.Root, plugins.DisabledMarker)); !os.IsNotExist(err) {
				return fmt.Errorf("disabled marker still present after enable: %v", err)
			}
			if err := plugins.Remove(workspace, installed.ID); err != nil {
				return err
			}
			return nil
		},
		verify: func(_ string, result runloop.TurnResult, output string) error {
			if !strings.Contains(output, "plugin lifecycle harness ok") {
				return fmt.Errorf("missing plugin lifecycle final response")
			}
			if err := expectToolCalls(result, 0, false); err != nil {
				return err
			}
			for _, root := range []string{installedRoot, disabledRoot} {
				if strings.TrimSpace(root) == "" {
					return fmt.Errorf("missing lifecycle plugin root")
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					return fmt.Errorf("plugin root still exists after remove: %s", root)
				}
			}
			return nil
		},
	}
}

func taskLifecycleScenario() scenario {
	return scenario{
		name: "task_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{ConfigHome: configHome})

			createOut, err := registry.Execute(ctx, "TaskCreateTool", json.RawMessage(`{
				"command": "printf task-output",
				"kind": "parity",
				"session_id": "session-task"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var created struct {
				TaskID string          `json:"task_id"`
				Kind   string          `json:"kind"`
				Task   background.Task `json:"task"`
			}
			if err := json.Unmarshal([]byte(createOut), &created); err != nil {
				return localScenarioResult{}, err
			}
			if created.TaskID == "" || created.Task.ID != created.TaskID || created.Kind != "parity" {
				return localScenarioResult{}, fmt.Errorf("unexpected task create output: %s", createOut)
			}

			statusOut, err := registry.Execute(ctx, "task_status", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var status struct {
				TaskID string `json:"task_id"`
				Kind   string `json:"kind"`
			}
			if err := json.Unmarshal([]byte(statusOut), &status); err != nil {
				return localScenarioResult{}, err
			}
			if status.TaskID != created.TaskID || status.Kind != "parity" {
				return localScenarioResult{}, fmt.Errorf("unexpected task status output: %s", statusOut)
			}

			outputOut, err := registry.Execute(ctx, "TaskOutputTool", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 2000
			}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var output struct {
				TaskID        string `json:"task_id"`
				Status        string `json:"status"`
				Stdout        string `json:"stdout"`
				HasOutput     bool   `json:"has_output"`
				RawOutputPath string `json:"rawOutputPath"`
			}
			if err := json.Unmarshal([]byte(outputOut), &output); err != nil {
				return localScenarioResult{}, err
			}
			if output.TaskID != created.TaskID || !output.HasOutput || output.Stdout != "task-output" {
				return localScenarioResult{}, fmt.Errorf("unexpected task output: %s", outputOut)
			}
			if _, err := os.Stat(output.RawOutputPath); err != nil {
				return localScenarioResult{}, fmt.Errorf("task raw output path missing: %w", err)
			}

			updateOut, err := registry.Execute(ctx, "task_update", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"message": "review logs"
			}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var updated struct {
				TaskID       string `json:"task_id"`
				MessageCount int    `json:"message_count"`
				LastMessage  string `json:"last_message"`
			}
			if err := json.Unmarshal([]byte(updateOut), &updated); err != nil {
				return localScenarioResult{}, err
			}
			if updated.TaskID != created.TaskID || updated.MessageCount != 1 || updated.LastMessage != "review logs" {
				return localScenarioResult{}, fmt.Errorf("unexpected task update output: %s", updateOut)
			}

			getOut, err := registry.Execute(ctx, "task_get", json.RawMessage(fmt.Sprintf(`{"id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetched struct {
				TaskID string `json:"task_id"`
				Task   struct {
					Messages []background.TaskMessage `json:"messages"`
				} `json:"task"`
			}
			if err := json.Unmarshal([]byte(getOut), &fetched); err != nil {
				return localScenarioResult{}, err
			}
			if fetched.TaskID != created.TaskID || len(fetched.Task.Messages) != 1 || fetched.Task.Messages[0].Message != "review logs" {
				return localScenarioResult{}, fmt.Errorf("unexpected task get output: %s", getOut)
			}

			listOut, err := registry.Execute(ctx, "task_list", json.RawMessage(`{"session_id":"session-task","kind":"parity"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listed struct {
				Total int `json:"total"`
				Tasks []struct {
					TaskID string `json:"task_id"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
				return localScenarioResult{}, err
			}
			if listed.Total != 1 || len(listed.Tasks) != 1 || listed.Tasks[0].TaskID != created.TaskID {
				return localScenarioResult{}, fmt.Errorf("unexpected task list output: %s", listOut)
			}

			stopCreateOut, err := registry.Execute(ctx, "task_create", json.RawMessage(`{
				"command": "printf task-stop-ready; sleep 5",
				"kind": "parity",
				"session_id": "session-task"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var stopCreated struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal([]byte(stopCreateOut), &stopCreated); err != nil {
				return localScenarioResult{}, err
			}
			if stopCreated.TaskID == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected stoppable task output: %s", stopCreateOut)
			}
			defer func() {
				_, _ = registry.Execute(ctx, "task_stop", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, stopCreated.TaskID)), nil)
			}()

			stopReadyOut, err := registry.Execute(ctx, "task_output", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 2000
			}`, stopCreated.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(stopReadyOut, "task-stop-ready") {
				return localScenarioResult{}, fmt.Errorf("stoppable task did not produce readiness output: %s", stopReadyOut)
			}
			stopOut, err := registry.Execute(ctx, "TaskStopTool", json.RawMessage(fmt.Sprintf(`{"shell_id":%q}`, stopCreated.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var stopped struct {
				TaskID      string `json:"task_id"`
				Status      string `json:"status"`
				Message     string `json:"message"`
				Interrupted bool   `json:"interrupted"`
			}
			if err := json.Unmarshal([]byte(stopOut), &stopped); err != nil {
				return localScenarioResult{}, err
			}
			if stopped.TaskID != stopCreated.TaskID || stopped.Status != "stopped" || stopped.Message != "Task stopped" {
				return localScenarioResult{}, fmt.Errorf("unexpected task stop output: %s", stopOut)
			}

			report := map[string]any{
				"kind": "task_lifecycle",
				"task": map[string]any{
					"id":           created.TaskID,
					"status":       output.Status,
					"stdout":       output.Stdout,
					"message":      updated.LastMessage,
					"listed_total": listed.Total,
					"raw_output":   filepath.Base(output.RawOutputPath),
				},
				"stopped": map[string]any{
					"id":          stopped.TaskID,
					"status":      stopped.Status,
					"interrupted": stopped.Interrupted,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "task lifecycle harness ok",
				RequestCount: 9,
				MessageCount: 1,
				ToolCalls:    9,
				ToolUses: []string{
					"task_create",
					"task_status",
					"task_output",
					"task_update",
					"task_get",
					"task_list",
					"task_create",
					"task_output",
					"task_stop",
				},
			}, nil
		},
	}
}

func taskPacketRoundtripScenario() scenario {
	return scenario{
		name: "task_packet_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			moduleDir := filepath.Join(workspace, "internal", "taskpacket")
			if err := os.MkdirAll(moduleDir, 0o755); err != nil {
				return localScenarioResult{}, err
			}
			if err := os.WriteFile(filepath.Join(moduleDir, "taskpacket.go"), []byte("package taskpacket\n"), 0o644); err != nil {
				return localScenarioResult{}, err
			}
			shim := filepath.Join(workspace, "task-packet-shim")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'packet:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				ConfigHome: configHome,
				Executable: shim,
			})

			runOut, err := registry.Execute(ctx, "RunTaskPacketTool", json.RawMessage(`{
				"objective": "Implement typed task packet parity",
				"scope": "module",
				"scope_path": "internal/taskpacket",
				"repo": "codog",
				"worktree": "reviewer",
				"branch_policy": "main only",
				"acceptance_tests": ["go test ./internal/taskpacket"],
				"acceptance_criteria": ["packet validates", "packet persists"],
				"resources": [{"kind": "module", "value": "internal/taskpacket"}],
				"model": "claude-test",
				"provider": "anthropic",
				"permission_profile": "workspace-write",
				"commit_policy": "single focused commit",
				"reporting_targets": ["leader"],
				"recovery_policy": "retry once with narrowed scope",
				"verification_plan": ["go test ./internal/taskpacket"]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var created struct {
				TaskID        string `json:"task_id"`
				Status        string `json:"status"`
				Description   string `json:"description"`
				Prompt        string `json:"prompt"`
				ResolvedScope struct {
					Scope        string `json:"scope"`
					Path         string `json:"path"`
					AbsolutePath string `json:"absolute_path"`
				} `json:"resolved_scope"`
				TaskPacket struct {
					Objective          string   `json:"objective"`
					Scope              string   `json:"scope"`
					ScopePath          string   `json:"scope_path"`
					Repo               string   `json:"repo"`
					Worktree           string   `json:"worktree"`
					AcceptanceCriteria []string `json:"acceptance_criteria"`
					Model              string   `json:"model"`
					Provider           string   `json:"provider"`
					PermissionProfile  string   `json:"permission_profile"`
					ReportingTargets   []string `json:"reporting_targets"`
					VerificationPlan   []string `json:"verification_plan"`
				} `json:"task_packet"`
				Task background.Task `json:"task"`
			}
			if err := json.Unmarshal([]byte(runOut), &created); err != nil {
				return localScenarioResult{}, err
			}
			if created.TaskID == "" || created.Status != "running" || created.Description != "Implement typed task packet parity" {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet create output: %s", runOut)
			}
			if created.Task.Kind != "task_packet" || created.Task.ID != created.TaskID || len(created.Task.TaskPacket) == 0 {
				return localScenarioResult{}, fmt.Errorf("task packet metadata was not persisted on task: %s", runOut)
			}
			if created.ResolvedScope.Scope != "module" || created.ResolvedScope.Path != "internal/taskpacket" || filepath.Clean(created.ResolvedScope.AbsolutePath) != filepath.Clean(moduleDir) {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet scope resolution: %s", runOut)
			}
			if created.TaskPacket.Objective != "Implement typed task packet parity" ||
				created.TaskPacket.Scope != "module" ||
				created.TaskPacket.Repo != "codog" ||
				created.TaskPacket.Worktree != "reviewer" ||
				created.TaskPacket.Model != "claude-test" ||
				created.TaskPacket.Provider != "anthropic" ||
				created.TaskPacket.PermissionProfile != "workspace-write" ||
				!slices.Equal(created.TaskPacket.AcceptanceCriteria, []string{"packet validates", "packet persists"}) ||
				!slices.Equal(created.TaskPacket.ReportingTargets, []string{"leader"}) ||
				!slices.Equal(created.TaskPacket.VerificationPlan, []string{"go test ./internal/taskpacket"}) {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet payload: %s", runOut)
			}
			var persisted map[string]any
			if err := json.Unmarshal(created.Task.TaskPacket, &persisted); err != nil {
				return localScenarioResult{}, err
			}
			if persisted["objective"] != "Implement typed task packet parity" || persisted["scope"] != "module" || persisted["provider"] != "anthropic" {
				return localScenarioResult{}, fmt.Errorf("unexpected persisted task packet: %#v", persisted)
			}

			outputOut, err := registry.Execute(ctx, "task_output", json.RawMessage(fmt.Sprintf(`{
				"task_id": %q,
				"block": true,
				"timeout_ms": 10000
			}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			if !strings.Contains(outputOut, "packet:prompt") || !strings.Contains(outputOut, "Implement typed task packet parity") || !strings.Contains(outputOut, "Verification plan:") {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet output: %s", outputOut)
			}

			getOut, err := registry.Execute(ctx, "task_get", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetched struct {
				TaskID string          `json:"task_id"`
				Task   background.Task `json:"task"`
			}
			if err := json.Unmarshal([]byte(getOut), &fetched); err != nil {
				return localScenarioResult{}, err
			}
			if fetched.TaskID != created.TaskID || fetched.Task.Kind != "task_packet" || len(fetched.Task.TaskPacket) == 0 {
				return localScenarioResult{}, fmt.Errorf("unexpected fetched task packet task: %s", getOut)
			}

			stopOut, err := registry.Execute(ctx, "task_stop", json.RawMessage(fmt.Sprintf(`{"task_id":%q}`, created.TaskID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var stopped struct {
				TaskID string `json:"task_id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(stopOut), &stopped); err != nil {
				return localScenarioResult{}, err
			}
			if stopped.TaskID != created.TaskID || stopped.Status != "stopped" {
				return localScenarioResult{}, fmt.Errorf("unexpected task packet stop output: %s", stopOut)
			}

			report := map[string]any{
				"kind": "task_packet_roundtrip",
				"task_packet": map[string]any{
					"task_id":            created.TaskID,
					"scope":              created.TaskPacket.Scope,
					"scope_path":         created.TaskPacket.ScopePath,
					"repo":               created.TaskPacket.Repo,
					"model":              created.TaskPacket.Model,
					"provider":           created.TaskPacket.Provider,
					"permission_profile": created.TaskPacket.PermissionProfile,
					"criteria":           len(created.TaskPacket.AcceptanceCriteria),
					"verification_steps": len(created.TaskPacket.VerificationPlan),
					"stopped":            stopped.Status,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "task packet harness ok",
				RequestCount: 4,
				MessageCount: 1,
				ToolCalls:    4,
				ToolUses: []string{
					"run_task_packet",
					"task_output",
					"task_get",
					"task_stop",
				},
			}, nil
		},
	}
}

func teamCronLifecycleScenario() scenario {
	return scenario{
		name: "team_cron_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			shim := filepath.Join(workspace, "team-shim")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'team-shim:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				ConfigHome: configHome,
				Executable: shim,
			})

			teamCreateOut, err := registry.Execute(ctx, "TeamCreateTool", json.RawMessage(`{
				"name": "review",
				"session_id": "session-team",
				"tasks": [
					{"description": "auth", "prompt": "check auth flow"},
					{"description": "tests", "prompt": "check test suite"}
				]
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var createdTeam struct {
				ID        string   `json:"team_id"`
				Name      string   `json:"name"`
				TaskCount int      `json:"task_count"`
				TaskIDs   []string `json:"task_ids"`
				Status    string   `json:"status"`
			}
			if err := json.Unmarshal([]byte(teamCreateOut), &createdTeam); err != nil {
				return localScenarioResult{}, err
			}
			if createdTeam.ID == "" || createdTeam.Name != "review" || createdTeam.TaskCount != 2 || len(createdTeam.TaskIDs) != 2 || createdTeam.Status != "running" {
				return localScenarioResult{}, fmt.Errorf("unexpected team create output: %s", teamCreateOut)
			}
			defer func() {
				_, _ = registry.Execute(ctx, "team_delete", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
			}()

			taskStore := background.NewStore(configHome)
			if _, err := waitForBackgroundLogs(ctx, taskStore, createdTeam.TaskIDs[0], "Task: auth", 10*time.Second); err != nil {
				return localScenarioResult{}, err
			}
			if _, err := waitForBackgroundLogs(ctx, taskStore, createdTeam.TaskIDs[1], "check test suite", 10*time.Second); err != nil {
				return localScenarioResult{}, err
			}

			teamListOut, err := registry.Execute(ctx, "team_list", json.RawMessage(`{"status":"running"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listedTeams struct {
				Kind  string `json:"kind"`
				Total int    `json:"total"`
				Teams []struct {
					ID           string           `json:"team_id"`
					TaskStatuses []map[string]any `json:"task_statuses"`
				} `json:"teams"`
			}
			if err := json.Unmarshal([]byte(teamListOut), &listedTeams); err != nil {
				return localScenarioResult{}, err
			}
			if listedTeams.Kind != "team_list" || listedTeams.Total != 1 || len(listedTeams.Teams) != 1 || listedTeams.Teams[0].ID != createdTeam.ID || len(listedTeams.Teams[0].TaskStatuses) != 2 {
				return localScenarioResult{}, fmt.Errorf("unexpected team list output: %s", teamListOut)
			}

			teamGetOut, err := registry.Execute(ctx, "TeamGetTool", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetchedTeam struct {
				Kind      string `json:"kind"`
				ID        string `json:"team_id"`
				TaskCount int    `json:"task_count"`
				Tasks     []struct {
					Description string `json:"description"`
					Prompt      string `json:"prompt"`
				} `json:"tasks"`
			}
			if err := json.Unmarshal([]byte(teamGetOut), &fetchedTeam); err != nil {
				return localScenarioResult{}, err
			}
			if fetchedTeam.Kind != "team" || fetchedTeam.ID != createdTeam.ID || fetchedTeam.TaskCount != 2 || len(fetchedTeam.Tasks) != 2 || fetchedTeam.Tasks[0].Description != "auth" {
				return localScenarioResult{}, fmt.Errorf("unexpected team get output: %s", teamGetOut)
			}

			teamDeleteOut, err := registry.Execute(ctx, "team_delete", json.RawMessage(fmt.Sprintf(`{"team_id":%q}`, createdTeam.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var deletedTeam struct {
				ID           string   `json:"team_id"`
				Status       string   `json:"status"`
				StoppedTasks []string `json:"stopped_tasks"`
				Message      string   `json:"message"`
			}
			if err := json.Unmarshal([]byte(teamDeleteOut), &deletedTeam); err != nil {
				return localScenarioResult{}, err
			}
			if deletedTeam.ID != createdTeam.ID || deletedTeam.Status != "deleted" || deletedTeam.Message != "Team deleted" || len(deletedTeam.StoppedTasks) != 2 {
				return localScenarioResult{}, fmt.Errorf("unexpected team delete output: %s", teamDeleteOut)
			}

			cronCreateOut, err := registry.Execute(ctx, "CronCreateTool", json.RawMessage(`{
				"schedule": "0 9 * * 1",
				"prompt": "review weekly status",
				"description": "weekly review"
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var createdCron struct {
				ID          string `json:"cron_id"`
				Schedule    string `json:"schedule"`
				Prompt      string `json:"prompt"`
				Description string `json:"description"`
				Enabled     bool   `json:"enabled"`
			}
			if err := json.Unmarshal([]byte(cronCreateOut), &createdCron); err != nil {
				return localScenarioResult{}, err
			}
			if createdCron.ID == "" || createdCron.Schedule != "0 9 * * 1" || createdCron.Prompt != "review weekly status" || createdCron.Description != "weekly review" || !createdCron.Enabled {
				return localScenarioResult{}, fmt.Errorf("unexpected cron create output: %s", cronCreateOut)
			}

			cronListOut, err := registry.Execute(ctx, "cron_list", json.RawMessage(`{}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listedCrons struct {
				Count int `json:"count"`
				Crons []struct {
					ID       string `json:"cron_id"`
					Schedule string `json:"schedule"`
				} `json:"crons"`
			}
			if err := json.Unmarshal([]byte(cronListOut), &listedCrons); err != nil {
				return localScenarioResult{}, err
			}
			if listedCrons.Count != 1 || len(listedCrons.Crons) != 1 || listedCrons.Crons[0].ID != createdCron.ID || listedCrons.Crons[0].Schedule != createdCron.Schedule {
				return localScenarioResult{}, fmt.Errorf("unexpected cron list output: %s", cronListOut)
			}

			cronDeleteOut, err := registry.Execute(ctx, "CronDeleteTool", json.RawMessage(fmt.Sprintf(`{"cron_id":%q}`, createdCron.ID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var deletedCron struct {
				ID      string `json:"cron_id"`
				Status  string `json:"status"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(cronDeleteOut), &deletedCron); err != nil {
				return localScenarioResult{}, err
			}
			if deletedCron.ID != createdCron.ID || deletedCron.Status != "deleted" || deletedCron.Message != "Cron entry removed" {
				return localScenarioResult{}, fmt.Errorf("unexpected cron delete output: %s", cronDeleteOut)
			}

			report := map[string]any{
				"kind": "team_cron_lifecycle",
				"team": map[string]any{
					"id":            createdTeam.ID,
					"task_count":    createdTeam.TaskCount,
					"listed_total":  listedTeams.Total,
					"deleted":       deletedTeam.Status,
					"stopped_tasks": len(deletedTeam.StoppedTasks),
				},
				"cron": map[string]any{
					"id":       createdCron.ID,
					"schedule": createdCron.Schedule,
					"listed":   listedCrons.Count,
					"deleted":  deletedCron.Status,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "team cron lifecycle harness ok",
				RequestCount: 7,
				MessageCount: 1,
				ToolCalls:    7,
				ToolUses: []string{
					"team_create",
					"team_list",
					"team_get",
					"team_delete",
					"cron_create",
					"cron_list",
					"cron_delete",
				},
			}, nil
		},
	}
}

func workerLifecycleScenario() scenario {
	return scenario{
		name: "worker_lifecycle_roundtrip",
		runLocal: func(ctx context.Context, workspace string) (localScenarioResult, error) {
			configHome := filepath.Join(workspace, "config-home")
			shim := filepath.Join(workspace, "worker-shim")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf 'worker:%s\\n' \"$*\"\nsleep 5\n"), 0o755); err != nil {
				return localScenarioResult{}, err
			}
			registry := tools.NewRegistryWithOptions(workspace, tools.RegistryOptions{
				ConfigHome:   configHome,
				Executable:   shim,
				TrustedRoots: []string{"repo-default", "shared"},
			})

			createOut, err := registry.Execute(ctx, "WorkerCreateTool", json.RawMessage(`{
				"cwd": ".",
				"trusted_roots": ["shared", "."],
				"auto_recover_prompt_misdelivery": false
			}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var created struct {
				WorkerID                     string   `json:"worker_id"`
				Status                       string   `json:"status"`
				ReadyForPrompt               bool     `json:"ready_for_prompt"`
				TrustedRoots                 []string `json:"trusted_roots"`
				AutoRecoverPromptMisdelivery bool     `json:"auto_recover_prompt_misdelivery"`
			}
			if err := json.Unmarshal([]byte(createOut), &created); err != nil {
				return localScenarioResult{}, err
			}
			if created.WorkerID == "" || created.Status != "ready_for_prompt" || !created.ReadyForPrompt || created.AutoRecoverPromptMisdelivery || !slices.Equal(created.TrustedRoots, []string{"repo-default", "shared", "."}) {
				return localScenarioResult{}, fmt.Errorf("unexpected worker create output: %s", createOut)
			}
			defer func() {
				_, _ = registry.Execute(ctx, "worker_terminate", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			}()

			listOut, err := registry.Execute(ctx, "worker_list", json.RawMessage(`{"status":"ready_for_prompt"}`), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var listed struct {
				Kind    string `json:"kind"`
				Total   int    `json:"total"`
				Workers []struct {
					WorkerID string `json:"worker_id"`
				} `json:"workers"`
			}
			if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
				return localScenarioResult{}, err
			}
			if listed.Kind != "worker_list" || listed.Total != 1 || len(listed.Workers) != 1 || listed.Workers[0].WorkerID != created.WorkerID {
				return localScenarioResult{}, fmt.Errorf("unexpected worker list output: %s", listOut)
			}

			readyOut, err := registry.Execute(ctx, "worker_await_ready", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var ready struct {
				WorkerID       string `json:"worker_id"`
				Status         string `json:"status"`
				ReadyForPrompt bool   `json:"ready_for_prompt"`
			}
			if err := json.Unmarshal([]byte(readyOut), &ready); err != nil {
				return localScenarioResult{}, err
			}
			if ready.WorkerID != created.WorkerID || ready.Status != "ready_for_prompt" || !ready.ReadyForPrompt {
				return localScenarioResult{}, fmt.Errorf("unexpected worker ready output: %s", readyOut)
			}

			observeOut, err := registry.Execute(ctx, "WorkerObserveTool", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"screen_text": "Do you trust this folder?"
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var observed struct {
				Status         string `json:"status"`
				ReadyForPrompt bool   `json:"ready_for_prompt"`
			}
			if err := json.Unmarshal([]byte(observeOut), &observed); err != nil {
				return localScenarioResult{}, err
			}
			if observed.Status != "trust_prompt" || observed.ReadyForPrompt {
				return localScenarioResult{}, fmt.Errorf("unexpected worker observe output: %s", observeOut)
			}

			resolveOut, err := registry.Execute(ctx, "worker_resolve_trust", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var resolved struct {
				Status         string `json:"status"`
				ReadyForPrompt bool   `json:"ready_for_prompt"`
				TrustResolved  bool   `json:"trust_resolved"`
			}
			if err := json.Unmarshal([]byte(resolveOut), &resolved); err != nil {
				return localScenarioResult{}, err
			}
			if resolved.Status != "ready_for_prompt" || !resolved.ReadyForPrompt || !resolved.TrustResolved {
				return localScenarioResult{}, fmt.Errorf("unexpected worker trust resolution output: %s", resolveOut)
			}

			sendOut, err := registry.Execute(ctx, "worker_send_prompt", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"prompt": "implement worker tests",
				"task_receipt": {
					"repo": "codog",
					"task_kind": "test",
					"source_surface": "tool",
					"objective_preview": "implement worker tests"
				}
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var sent struct {
				Status      string `json:"status"`
				TaskID      string `json:"task_id"`
				TaskReceipt struct {
					Repo             string `json:"repo"`
					TaskKind         string `json:"task_kind"`
					SourceSurface    string `json:"source_surface"`
					ObjectivePreview string `json:"objective_preview"`
				} `json:"task_receipt"`
			}
			if err := json.Unmarshal([]byte(sendOut), &sent); err != nil {
				return localScenarioResult{}, err
			}
			if sent.Status != "running" || sent.TaskID == "" || sent.TaskReceipt.Repo != "codog" || sent.TaskReceipt.ObjectivePreview != "implement worker tests" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker send output: %s", sendOut)
			}
			if _, err := waitForBackgroundLogs(ctx, background.NewStore(configHome), sent.TaskID, "implement worker tests", 10*time.Second); err != nil {
				return localScenarioResult{}, err
			}

			getOut, err := registry.Execute(ctx, "WorkerGetTool", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var fetched struct {
				WorkerID   string `json:"worker_id"`
				Status     string `json:"status"`
				TaskID     string `json:"task_id"`
				TaskStatus string `json:"task_status"`
			}
			if err := json.Unmarshal([]byte(getOut), &fetched); err != nil {
				return localScenarioResult{}, err
			}
			if fetched.WorkerID != created.WorkerID || fetched.Status != "running" || fetched.TaskID != sent.TaskID || fetched.TaskStatus == "" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker get output: %s", getOut)
			}

			restartOut, err := registry.Execute(ctx, "worker_restart", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var restarted struct {
				Status string `json:"status"`
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal([]byte(restartOut), &restarted); err != nil {
				return localScenarioResult{}, err
			}
			if restarted.Status != "running" || restarted.TaskID == "" || restarted.TaskID == sent.TaskID {
				return localScenarioResult{}, fmt.Errorf("unexpected worker restart output: %s", restartOut)
			}

			completeOut, err := registry.Execute(ctx, "worker_observe_completion", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"finish_reason": "stop",
				"tokens_output": 12
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var completed struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(completeOut), &completed); err != nil {
				return localScenarioResult{}, err
			}
			if completed.Status != "finished" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker completion output: %s", completeOut)
			}

			timeoutOut, err := registry.Execute(ctx, "worker_startup_timeout", json.RawMessage(fmt.Sprintf(`{
				"worker_id": %q,
				"last_lifecycle_state": "trust_prompt",
				"pane_command": "codog repl",
				"transport_healthy": true,
				"mcp_healthy": true,
				"elapsed_seconds": 42,
				"trust_prompt_detected": true
			}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var timedOut struct {
				Status            string `json:"status"`
				LastError         string `json:"last_error"`
				StartupNoEvidence struct {
					Classification string `json:"classification"`
				} `json:"startup_no_evidence"`
			}
			if err := json.Unmarshal([]byte(timeoutOut), &timedOut); err != nil {
				return localScenarioResult{}, err
			}
			if timedOut.Status != "failed" || timedOut.LastError != "startup_no_evidence: trust_required" || timedOut.StartupNoEvidence.Classification != "trust_required" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker startup timeout output: %s", timeoutOut)
			}

			terminateOut, err := registry.Execute(ctx, "WorkerTerminateTool", json.RawMessage(fmt.Sprintf(`{"worker_id":%q}`, created.WorkerID)), nil)
			if err != nil {
				return localScenarioResult{}, err
			}
			var terminated struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal([]byte(terminateOut), &terminated); err != nil {
				return localScenarioResult{}, err
			}
			if terminated.Status != "terminated" {
				return localScenarioResult{}, fmt.Errorf("unexpected worker terminate output: %s", terminateOut)
			}

			report := map[string]any{
				"kind": "worker_lifecycle",
				"worker": map[string]any{
					"id":                created.WorkerID,
					"trusted_roots":     created.TrustedRoots,
					"trust_resolved":    resolved.TrustResolved,
					"prompt_task":       sent.TaskID,
					"restarted_task":    restarted.TaskID,
					"completion_status": completed.Status,
					"startup_failure":   timedOut.StartupNoEvidence.Classification,
					"terminal_status":   terminated.Status,
				},
			}
			data, err := json.Marshal(report)
			if err != nil {
				return localScenarioResult{}, err
			}
			return localScenarioResult{
				Output:       string(data),
				FinalMessage: "worker lifecycle harness ok",
				RequestCount: 11,
				MessageCount: 1,
				ToolCalls:    11,
				ToolUses: []string{
					"worker_create",
					"worker_list",
					"worker_await_ready",
					"worker_observe",
					"worker_resolve_trust",
					"worker_send_prompt",
					"worker_get",
					"worker_restart",
					"worker_observe_completion",
					"worker_startup_timeout",
					"worker_terminate",
				},
			}, nil
		},
	}
}
