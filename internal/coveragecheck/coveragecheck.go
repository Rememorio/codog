package coveragecheck

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	profileLinePattern = regexp.MustCompile(`^(.+):(\d+)\.(\d+),(\d+)\.(\d+)\s+(\d+)\s+(\d+)$`)
	diffHunkPattern    = regexp.MustCompile(`^@@\s+-\d+(?:,\d+)?\s+\+(\d+)(?:,(\d+))?\s+@@`)
)

// Block describes one statement region from a Go coverage profile.
type Block struct {
	File      string
	StartLine int
	EndLine   int
	Count     int64
}

// ChangedLines maps repository-relative files to added line numbers.
type ChangedLines map[string]map[int]struct{}

// Location identifies one changed source line.
type Location struct {
	File string
	Line int
}

// Report summarizes coverage for coverable changed lines.
type Report struct {
	Covered   int
	Coverable int
	Percent   float64
	Uncovered []Location
}

// ParseProfile parses a Go coverage profile and normalizes paths relative to modulePath.
func ParseProfile(reader io.Reader, modulePath string) ([]Block, error) {
	scanner := bufio.NewScanner(reader)
	lineNumber := 0
	var blocks []Block
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		matches := profileLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return nil, fmt.Errorf("coverage profile line %d is malformed", lineNumber)
		}
		startLine, _ := strconv.Atoi(matches[2])
		endLine, _ := strconv.Atoi(matches[4])
		endColumn, _ := strconv.Atoi(matches[5])
		count, _ := strconv.ParseInt(matches[7], 10, 64)
		if endColumn == 1 && endLine > startLine {
			endLine--
		}
		blocks = append(blocks, Block{
			File:      normalizeProfilePath(matches[1], modulePath),
			StartLine: startLine,
			EndLine:   endLine,
			Count:     count,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read coverage profile: %w", err)
	}
	return blocks, nil
}

// ParseDiff extracts added lines from a zero-context unified Git diff.
func ParseDiff(reader io.Reader) (ChangedLines, error) {
	changed := ChangedLines{}
	scanner := bufio.NewScanner(reader)
	currentFile := ""
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if strings.HasPrefix(line, "+++ ") {
			path, err := parseDiffPath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")))
			if err != nil {
				return nil, fmt.Errorf("diff line %d: %w", lineNumber, err)
			}
			currentFile = path
			continue
		}
		if !strings.HasPrefix(line, "@@") {
			continue
		}
		if currentFile == "" {
			return nil, fmt.Errorf("diff line %d has a hunk without a destination file", lineNumber)
		}
		matches := diffHunkPattern.FindStringSubmatch(line)
		if matches == nil {
			return nil, fmt.Errorf("diff line %d has a malformed hunk header", lineNumber)
		}
		start, _ := strconv.Atoi(matches[1])
		count := 1
		if matches[2] != "" {
			count, _ = strconv.Atoi(matches[2])
		}
		if count == 0 || currentFile == "/dev/null" {
			continue
		}
		if changed[currentFile] == nil {
			changed[currentFile] = map[int]struct{}{}
		}
		for addedLine := start; addedLine < start+count; addedLine++ {
			changed[currentFile][addedLine] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Git diff: %w", err)
	}
	return changed, nil
}

// Evaluate reports coverage for changed lines intersecting statement blocks.
func Evaluate(blocks []Block, changed ChangedLines) Report {
	byFile := map[string][]Block{}
	for _, block := range blocks {
		byFile[filepath.ToSlash(block.File)] = append(byFile[filepath.ToSlash(block.File)], block)
	}
	files := make([]string, 0, len(changed))
	for file := range changed {
		files = append(files, filepath.ToSlash(file))
	}
	sort.Strings(files)

	report := Report{}
	for _, file := range files {
		lines := make([]int, 0, len(changed[file]))
		for line := range changed[file] {
			lines = append(lines, line)
		}
		sort.Ints(lines)
		for _, line := range lines {
			coverable, covered := lineCoverage(byFile[file], line)
			if !coverable {
				continue
			}
			report.Coverable++
			if covered {
				report.Covered++
				continue
			}
			report.Uncovered = append(report.Uncovered, Location{File: file, Line: line})
		}
	}
	if report.Coverable == 0 {
		report.Percent = 100
	} else {
		report.Percent = float64(report.Covered) * 100 / float64(report.Coverable)
	}
	return report
}

// MeetsThreshold reports whether the changed-line coverage meets threshold.
func (r Report) MeetsThreshold(threshold float64) bool {
	return r.Percent+1e-9 >= threshold
}

func lineCoverage(blocks []Block, line int) (coverable bool, covered bool) {
	for _, block := range blocks {
		if line < block.StartLine || line > block.EndLine {
			continue
		}
		coverable = true
		if block.Count > 0 {
			covered = true
		}
	}
	return coverable, covered
}

func normalizeProfilePath(path string, modulePath string) string {
	path = filepath.ToSlash(strings.TrimSpace(path))
	modulePath = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(modulePath)), "/")
	if modulePath != "" {
		path = strings.TrimPrefix(path, modulePath+"/")
	}
	return strings.TrimPrefix(path, "./")
}

func parseDiffPath(value string) (string, error) {
	if strings.HasPrefix(value, `"`) {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted destination path: %w", err)
		}
		value = decoded
	}
	if value == "/dev/null" {
		return value, nil
	}
	value = strings.TrimPrefix(value, "b/")
	if value == "" {
		return "", fmt.Errorf("destination path is empty")
	}
	return filepath.ToSlash(value), nil
}
