package agent

import "strings"

type oauthArgumentCount struct {
	minimum int
	maximum int
}

func (c oauthArgumentCount) accepts(count int) bool {
	if count < c.minimum {
		return false
	}
	return c.maximum == 0 || count <= c.maximum
}

func resumedOAuthRoute(args []string) (string, bool) {
	action := strings.ToLower(strings.TrimSpace(args[0]))
	switch action {
	case "discover":
		return resumedSlashCommandLabel("/oauth", action), len(args) == 2
	case "pkce":
		return resumedSlashCommandLabel("/oauth", action), len(args) == 1
	case "status", "logout":
		return resumedSlashCommandLabel("/oauth", action), len(args) <= 2
	case "provider":
		return resumedOAuthSubcommandRoute(args, "/oauth provider", oauthProviderArgumentCounts, false)
	case "token":
		return resumedOAuthSubcommandRoute(args, "/oauth token", oauthTokenArgumentCounts, true)
	case "device":
		return resumedOAuthSubcommandRoute(args, "/oauth device", oauthDeviceArgumentCounts, false)
	case "browser":
		return resumedOAuthSubcommandRoute(args, "/oauth browser", oauthBrowserArgumentCounts, false)
	default:
		return resumedSlashCommandLabel("/oauth", action), false
	}
}

func resumedOAuthSubcommandRoute(args []string, prefix string, rules map[string]oauthArgumentCount, preserveLabel bool) (string, bool) {
	if len(args) < 2 {
		return prefix, false
	}
	rawAction := args[1]
	action := strings.ToLower(strings.TrimSpace(rawAction))
	labelAction := action
	if preserveLabel {
		labelAction = rawAction
	}
	label := resumedSlashCommandLabel(prefix, labelAction)
	rule, ok := rules[action]
	return label, ok && rule.accepts(len(args))
}

var oauthProviderArgumentCounts = map[string]oauthArgumentCount{
	"save":   {minimum: 5},
	"list":   {minimum: 2, maximum: 2},
	"show":   {minimum: 3, maximum: 3},
	"delete": {minimum: 3, maximum: 3},
}

var oauthTokenArgumentCounts = map[string]oauthArgumentCount{
	"show":    {minimum: 2, maximum: 2},
	"status":  {minimum: 2, maximum: 3},
	"refresh": {minimum: 2, maximum: 3},
	"save":    {minimum: 3, maximum: 5},
	"delete":  {minimum: 2, maximum: 2},
	"revoke":  {minimum: 2, maximum: 4},
}

var oauthDeviceArgumentCounts = map[string]oauthArgumentCount{
	"status": {minimum: 2, maximum: 3},
	"start":  {minimum: 3},
	"poll":   {minimum: 4},
	"login":  {minimum: 3},
}

var oauthBrowserArgumentCounts = map[string]oauthArgumentCount{
	"status":   {minimum: 2, maximum: 3},
	"start":    {minimum: 4},
	"exchange": {minimum: 6},
	"login":    {minimum: 3, maximum: 4},
}
