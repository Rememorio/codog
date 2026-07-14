package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Rememorio/codog/internal/oauth"
)

func (a *App) oauthPKCE() error {
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, pkce)
}

func (a *App) oauthDiscover(args []string) error {
	if len(args) < 2 {
		return renderMissingActionArgument(a.Out, "oauth", "discover", "issuer_url", "oauth discover requires an issuer URL", "Usage: codog oauth discover ISSUER_URL [--json|--output-format json].", "json")
	}
	metadata, err := oauth.DiscoverProvider(context.Background(), args[1])
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, metadata)
}

func (a *App) oauthStatus(profile string) error {
	return writeIndentedJSON(a.Out, oauth.InspectStatus(a.Config.ConfigHome, profile, time.Now().UTC()))
}

func (a *App) oauthLogout(profile string) error {
	result, err := oauth.Logout(context.Background(), a.Config.ConfigHome, profile)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, result)
}

func (a *App) oauthToken(args []string) error {
	if len(args) == 0 {
		return a.oauthStatus("")
	}
	switch args[0] {
	case "save":
		return a.oauthTokenSave(args)
	case "show":
		return a.oauthTokenShow()
	case "status":
		return a.oauthStatus(optionalArgument(args, 1))
	case "refresh":
		return a.oauthTokenRefresh(optionalArgument(args, 1))
	case "revoke":
		return a.oauthTokenRevokeCommand(args[1:])
	case "delete":
		return a.oauthTokenDelete()
	default:
		return unexpectedExtraArgsError{Command: "oauth token", Args: []string{args[0]}, Usage: oauthTokenUsage}
	}
}

func (a *App) oauthTokenSave(args []string) error {
	if len(args) < 2 {
		return renderMissingActionArgument(a.Out, "oauth", "token_save", "access_token", "oauth token save requires an access token", "Usage: codog oauth token save ACCESS_TOKEN [REFRESH_TOKEN] [EXPIRES_AT].", "json")
	}
	token := oauth.Token{AccessToken: args[1]}
	if len(args) > 2 {
		token.RefreshToken = args[2]
	}
	if len(args) > 3 {
		expiresAt, err := time.Parse(time.RFC3339, args[3])
		if err != nil {
			return err
		}
		token.ExpiresAt = expiresAt
	}
	saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, saved.View(time.Now().UTC()))
}

func (a *App) oauthTokenShow() error {
	token, err := oauth.LoadToken(a.Config.ConfigHome)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, token.View(time.Now().UTC()))
}

func (a *App) oauthTokenRefresh(profile string) error {
	token, err := oauth.RefreshStoredToken(context.Background(), a.Config.ConfigHome, profile)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, token.View(time.Now().UTC()))
}

func (a *App) oauthTokenRevokeCommand(args []string) error {
	result, err := a.oauthTokenRevoke(args)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, result)
}

func (a *App) oauthTokenDelete() error {
	if err := oauth.DeleteToken(a.Config.ConfigHome); err != nil {
		return err
	}
	report := oauthTokenDeleteReport{Kind: "oauth_token", Action: "delete", Status: "ok", Deleted: true}
	return writeIndentedJSON(a.Out, report)
}

func (a *App) oauthBrowserStart(args []string) error {
	if len(args) < 1 {
		return renderMissingActionArgument(a.Out, "oauth", "browser_start", "profile", "oauth browser start requires a profile name", "Usage: codog oauth browser start PROFILE REDIRECT_URI [SCOPE...].", "json")
	}
	if len(args) < 2 {
		return renderMissingActionArgument(a.Out, "oauth", "browser_start", "redirect_uri", "oauth browser start requires a redirect URI", "Usage: codog oauth browser start PROFILE REDIRECT_URI [SCOPE...].", "json")
	}
	source, err := a.oauthProfileSource(args[0], args[2:])
	if err != nil {
		return err
	}
	auth, err := oauth.BuildBrowserAuthorization(source.Metadata, source.ClientID, args[1], source.Scopes, "", oauth.PKCE{})
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, auth)
}

func (a *App) oauthBrowserExchange(args []string) error {
	missing := []struct {
		argument string
		message  string
	}{
		{"profile", "oauth browser exchange requires a profile name"},
		{"code", "oauth browser exchange requires an authorization code"},
		{"code_verifier", "oauth browser exchange requires a PKCE code verifier"},
		{"redirect_uri", "oauth browser exchange requires a redirect URI"},
	}
	if len(args) < len(missing) {
		item := missing[len(args)]
		return renderMissingActionArgument(a.Out, "oauth", "browser_exchange", item.argument, item.message, "Usage: codog oauth browser exchange PROFILE CODE CODE_VERIFIER REDIRECT_URI.", "json")
	}
	source, err := a.oauthProfileSource(args[0], nil)
	if err != nil {
		return err
	}
	token, err := oauth.ExchangeAuthorizationCode(context.Background(), source.Metadata, source.ClientID, args[1], args[2], args[3])
	if err != nil {
		return err
	}
	saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, saved.View(time.Now().UTC()))
}

func (a *App) oauthBrowserLogin(args []string) error {
	if len(args) < 1 {
		return renderMissingActionArgument(a.Out, "oauth", "browser_login", "profile", "oauth browser login requires a profile name", "Usage: codog oauth browser login PROFILE [ADDR].", "json")
	}
	source, err := a.oauthProfileSource(args[0], nil)
	if err != nil {
		return err
	}
	addr := "127.0.0.1:0"
	if len(args) > 1 {
		addr = args[1]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return err
	}
	state, err := oauth.GenerateState()
	if err != nil {
		return err
	}
	callback, err := startBrowserCallbackServer(ctx, addr, "/oauth/callback", state)
	if err != nil {
		return err
	}
	defer func() { _ = callback.Close() }()
	auth, err := oauth.BuildBrowserAuthorization(source.Metadata, source.ClientID, callback.RedirectURI, source.Scopes, state, pkce)
	if err != nil {
		return err
	}
	if a.Err != nil {
		fmt.Fprintf(a.Err, "Open %s\n", auth.AuthorizationURL)
	}
	result := <-callback.Results
	if result.Err != nil {
		return result.Err
	}
	token, err := oauth.ExchangeAuthorizationCode(ctx, source.Metadata, source.ClientID, result.Callback.Code, auth.CodeVerifier, callback.RedirectURI)
	if err != nil {
		return err
	}
	saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, map[string]any{"redirect_uri": callback.RedirectURI, "token": saved.View(time.Now().UTC())})
}

func (a *App) oauthDeviceStart(args []string) error {
	if len(args) < 1 {
		return renderMissingActionArgument(a.Out, "oauth", "device_start", "profile_or_issuer", "oauth device start requires a profile name or issuer URL", "Usage: codog oauth device start ISSUER_URL CLIENT_ID [SCOPE...] or codog oauth device start PROFILE [SCOPE...].", "json")
	}
	if isURLish(args[0]) && len(args) < 2 {
		return renderMissingActionArgument(a.Out, "oauth", "device_start", "client_id", "oauth device start with an issuer URL requires a client id", "Usage: codog oauth device start ISSUER_URL CLIENT_ID [SCOPE...].", "json")
	}
	source, err := a.oauthDeviceSource(args, true)
	if err != nil {
		return err
	}
	auth, err := oauth.StartDeviceAuthorization(context.Background(), source.Metadata, source.ClientID, source.Scopes)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, auth)
}

func (a *App) oauthDevicePoll(args []string) error {
	if err := a.validateOAuthDevicePollArgs(args); err != nil {
		return err
	}
	source, deviceCode, err := a.oauthDevicePollSource(args)
	if err != nil {
		return err
	}
	token, err := oauth.PollDeviceToken(context.Background(), source.Metadata, deviceCode, oauth.DevicePollOptions{ClientID: source.ClientID})
	if err != nil {
		return err
	}
	saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, saved.View(time.Now().UTC()))
}

func (a *App) validateOAuthDevicePollArgs(args []string) error {
	if len(args) < 1 {
		return renderMissingActionArgument(a.Out, "oauth", "device_poll", "profile_or_issuer", "oauth device poll requires a profile name or issuer URL", "Usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE or codog oauth device poll PROFILE DEVICE_CODE.", "json")
	}
	if isURLish(args[0]) && len(args) < 2 {
		return renderMissingActionArgument(a.Out, "oauth", "device_poll", "client_id", "oauth device poll with an issuer URL requires a client id", "Usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE.", "json")
	}
	if isURLish(args[0]) && len(args) < 3 {
		return renderMissingActionArgument(a.Out, "oauth", "device_poll", "device_code", "oauth device poll requires a device code", "Usage: codog oauth device poll ISSUER_URL CLIENT_ID DEVICE_CODE.", "json")
	}
	if !isURLish(args[0]) && len(args) < 2 {
		return renderMissingActionArgument(a.Out, "oauth", "device_poll", "device_code", "oauth device poll requires a device code", "Usage: codog oauth device poll PROFILE DEVICE_CODE.", "json")
	}
	return nil
}

func (a *App) oauthDeviceLogin(args []string) error {
	if len(args) < 1 {
		return renderMissingActionArgument(a.Out, "oauth", "device_login", "profile_or_issuer", "oauth device login requires a profile name or issuer URL", "Usage: codog oauth device login ISSUER_URL CLIENT_ID [SCOPE...] or codog oauth device login PROFILE [SCOPE...].", "json")
	}
	if isURLish(args[0]) && len(args) < 2 {
		return renderMissingActionArgument(a.Out, "oauth", "device_login", "client_id", "oauth device login with an issuer URL requires a client id", "Usage: codog oauth device login ISSUER_URL CLIENT_ID [SCOPE...].", "json")
	}
	source, err := a.oauthDeviceSource(args, true)
	if err != nil {
		return err
	}
	auth, err := oauth.StartDeviceAuthorization(context.Background(), source.Metadata, source.ClientID, source.Scopes)
	if err != nil {
		return err
	}
	if a.Err != nil {
		target := auth.VerificationURI
		if auth.VerificationURIComplete != "" {
			target = auth.VerificationURIComplete
		}
		fmt.Fprintf(a.Err, "Open %s and enter code %s\n", target, auth.UserCode)
	}
	token, err := oauth.PollDeviceToken(context.Background(), source.Metadata, auth.DeviceCode, oauth.DevicePollOptions{
		ClientID: source.ClientID, Interval: time.Duration(auth.Interval) * time.Second, ExpiresAt: auth.ExpiresAt,
	})
	if err != nil {
		return err
	}
	saved, err := oauth.SaveToken(a.Config.ConfigHome, token)
	if err != nil {
		return err
	}
	return writeIndentedJSON(a.Out, map[string]any{"device": auth, "token": saved.View(time.Now().UTC())})
}

func optionalArgument(args []string, index int) string {
	if index >= len(args) {
		return ""
	}
	return args[index]
}

func writeIndentedJSON(out io.Writer, value any) error {
	data, _ := json.MarshalIndent(value, "", "  ")
	fmt.Fprintln(out, string(data))
	return nil
}
