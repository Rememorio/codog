package tui

import (
	"fmt"
	"strings"
)

func renderQuestionRequest(request QuestionRequest, questionIndex int, selected int, custom bool, selections [][]bool, customValues []string, width int, themed ...themeStyles) string {
	styles := resolveThemeStyles(themed)
	limit := composerLimit(width)
	questions := normalizedQuestions(request)
	questionIndex = min(max(questionIndex, 0), len(questions))
	lines := []string{styles.role("question").Render("Questions")}
	lines = appendQuestionTabs(lines, questions, questionIndex, selections, customValues, limit, styles)
	if questionIndex == len(questions) {
		return renderQuestionReview(lines, questions, selections, customValues, limit, styles)
	}
	lines = appendCurrentQuestion(lines, request, questions[questionIndex], questionIndex, selected, custom, selections, limit, styles)
	lines = append(lines, questionNavigationHint(questions[questionIndex], custom, limit, styles))
	return strings.Join(lines, "\n")
}

func composerLimit(width int) int {
	if width > 0 {
		return max(12, width-8)
	}
	return 100
}

func normalizedQuestions(request QuestionRequest) []Question {
	if len(request.Questions) > 0 {
		return request.Questions
	}
	options := make([]QuestionOption, 0, len(request.Choices))
	for _, choice := range request.Choices {
		options = append(options, QuestionOption{Label: choice})
	}
	return []Question{{Question: request.Question, Header: "Question", Options: options}}
}

func appendQuestionTabs(lines []string, questions []Question, current int, selections [][]bool, customValues []string, limit int, styles themeStyles) []string {
	if len(questions) <= 1 {
		return lines
	}
	tabs := make([]string, 0, len(questions)+1)
	for index, question := range questions {
		marker := "[ ]"
		if renderedQuestionAnswered(index, selections, customValues) {
			marker = "[x]"
		}
		prefix := ""
		if index == current {
			prefix = ">"
		}
		tabs = append(tabs, fmt.Sprintf("%s%s %s", prefix, marker, question.Header))
	}
	submitPrefix := ""
	if current == len(questions) {
		submitPrefix = ">"
	}
	tabs = append(tabs, submitPrefix+"Submit")
	return append(lines, styles.completionTitle().Render(truncateForComposer(strings.Join(tabs, "  "), limit)))
}

func renderQuestionReview(lines []string, questions []Question, selections [][]bool, customValues []string, limit int, styles themeStyles) string {
	lines = append(lines, styles.panelTitle().Render("Review answers"))
	for index, question := range questions {
		answer := renderedQuestionAnswer(index, question, selections, customValues)
		if answer == "" {
			answer = "(not answered)"
		}
		lines = append(lines, styles.completion().Render(truncateForComposer(question.Header+": "+answer, limit)))
	}
	lines = append(lines, styles.completion().Render(truncateForComposer("  Enter to submit · Left to go back · Esc to cancel", limit)))
	return strings.Join(lines, "\n")
}

func appendCurrentQuestion(lines []string, request QuestionRequest, question Question, questionIndex int, selected int, custom bool, selections [][]bool, limit int, styles themeStyles) []string {
	questionText := strings.TrimSpace(question.Question)
	if questionText == "" {
		questionText = "Choose an answer"
	}
	lines = append(lines, truncateForComposer(questionText, limit))
	if question.MultiSelect {
		lines = append(lines, styles.completionTitle().Render("Select one or more"))
	}
	if len(question.Options) == 0 {
		return append(lines, styles.selectedCompletion().Render("> Type something"))
	}
	selected = clampIndex(selected, len(question.Options)+1)
	for index := range question.Options {
		lines = append(lines, renderQuestionOption(request, question, questionIndex, index, selected, custom, selections, limit, styles))
	}
	lines = append(lines, renderCustomQuestionOption(question, selected, custom, styles))
	return appendSelectedQuestionDetails(lines, question, selected, limit, styles)
}

func renderQuestionOption(request QuestionRequest, question Question, questionIndex int, index int, selected int, custom bool, selections [][]bool, limit int, styles themeStyles) string {
	prefix := "  "
	style := styles.completion()
	if index == selected && !custom {
		prefix = "> "
		style = styles.selectedCompletion()
	}
	marker := questionOptionMarker(question, questionIndex, index, selections)
	label := fmt.Sprintf("%d. %s%s", index+1, marker, question.Options[index].Label)
	if strings.EqualFold(question.Options[index].Label, request.Default) {
		label += " (default)"
	}
	return style.Render(truncateForComposer(prefix+label, limit))
}

func questionOptionMarker(question Question, questionIndex int, optionIndex int, selections [][]bool) string {
	if !question.MultiSelect {
		return ""
	}
	if questionIndex < len(selections) && optionIndex < len(selections[questionIndex]) && selections[questionIndex][optionIndex] {
		return "[x] "
	}
	return "[ ] "
}

func renderCustomQuestionOption(question Question, selected int, custom bool, styles themeStyles) string {
	prefix := "  "
	style := styles.completion()
	if selected == len(question.Options) || custom {
		prefix = "> "
		style = styles.selectedCompletion()
	}
	return style.Render(prefix + "Type something")
}

func appendSelectedQuestionDetails(lines []string, question Question, selected int, limit int, styles themeStyles) []string {
	if selected >= len(question.Options) {
		return lines
	}
	option := question.Options[selected]
	if option.Description != "" {
		lines = append(lines, styles.completion().Render(truncateForComposer("  "+option.Description, limit)))
	}
	if option.Preview == "" {
		return lines
	}
	lines = append(lines, styles.completionTitle().Render("Preview"))
	for _, previewLine := range firstLines(option.Preview, 5) {
		lines = append(lines, styles.completion().Render(truncateForComposer("  "+previewLine, limit)))
	}
	return lines
}

func questionNavigationHint(question Question, custom bool, limit int, styles themeStyles) string {
	hint := "  Enter to select · Up/Down to navigate · Esc to cancel"
	if custom {
		hint = "  Type your response below, then press Enter"
	} else if question.MultiSelect {
		hint = "  Space to toggle · Enter next · Up/Down navigate · Esc cancel"
	}
	return styles.completion().Render(truncateForComposer(hint, limit))
}
