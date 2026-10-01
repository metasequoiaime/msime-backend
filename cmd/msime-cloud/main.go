// Command msime-cloud lets an AI assistant, or a person, use the 水杉云 API from a shell: list and describe its operations, sign in with an email or SMS code, and call any operation with the session attached and refreshed. Responses go to stdout; anything for a person goes to stderr.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const usage = `usage: msime-cloud <command> [arguments]

  routes [filter]                        list the operations: method, path, auth, summary
  describe METHOD PATH                   print one operation: parameters, request body and responses
  call METHOD PATH [json|-] [-q name=value]... [-F name=value|name=@file]... [-o file]
                                         call an operation and print its response
  login start --email ADDRESS | --phone +8613800138000
                                         send a sign-in code and print the challenge_id
  login finish --challenge ID --code CODE
                                         sign in with the code the user received and keep the session
  whoami                                 print the signed-in user
  logout [--all]                         end this session, or every session of the user, and forget it

PATH may be a template from routes or a concrete path. /v1 paths go to the API with the kept session (or MSIME_CLOUD_TOKEN) when the operation needs one; /api paths go to the admin site with MSIME_ADMIN_TOKEN. A request body is a JSON object given inline, or read from stdin with -; -F sends multipart form fields instead, @ reading a file. A response that is not JSON or text needs -o.

Environment:
  MSIME_CLOUD_URL         API base URL (default https://api.msime.app)
  MSIME_CLOUD_TOKEN       a device token or access token to send instead of the kept session
  MSIME_ADMIN_URL         admin site base URL (default https://admin.msime.app)
  MSIME_ADMIN_TOKEN       the admin key for /api paths
  MSIME_CLOUD_CONFIG_DIR  where the session is kept (default <user config dir>/msime-cloud)

Exit status: 0 for a 2xx response, 1 when the call fails or the server answers with an error (its body is still printed), 2 for a usage error.`

const (
	defaultServer      = "https://api.msime.app"
	defaultAdminServer = "https://admin.msime.app"
	requestTimeout     = 5 * time.Minute
)

type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

// statusError is a response outside 2xx, already printed.
type statusError struct{ status string }

func (e statusError) Error() string { return "the server answered " + e.status }

type cli struct {
	env    func(string) string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	client *http.Client
	now    func() time.Time
}

func main() {
	c := cli{env: os.Getenv, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, client: &http.Client{Timeout: requestTimeout}, now: time.Now}
	os.Exit(c.run(os.Args[1:]))
}

func (c cli) run(args []string) int {
	err := c.dispatch(args)
	var bad usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &bad):
		fmt.Fprintf(c.stderr, "msime-cloud: %s\n\n%s\n", err, usage)
		return 2
	default:
		fmt.Fprintf(c.stderr, "msime-cloud: %s\n", err)
		return 1
	}
}

func (c cli) dispatch(args []string) error {
	if len(args) == 0 {
		return usageError{"no command"}
	}
	command, args := args[0], args[1:]
	switch command {
	case "help", "-h", "--help":
		fmt.Fprintln(c.stdout, usage)
		return nil
	case "routes":
		if len(args) > 1 {
			return usageError{"routes takes at most one filter"}
		}
		return listRoutes(c.stdout, strings.Join(args, ""))
	case "describe":
		if len(args) != 2 {
			return usageError{"describe needs METHOD and PATH"}
		}
		return describe(c.stdout, args[0], args[1])
	case "call":
		return c.call(args)
	case "login":
		return c.login(args)
	case "whoami":
		if len(args) != 0 {
			return usageError{"whoami takes no arguments"}
		}
		return c.call([]string{"GET", "/v1/users/me"})
	case "logout":
		return c.logout(args)
	}
	return usageError{"unknown command " + command}
}

// request is one call as the command line describes it.
type request struct {
	method, path string
	query        url.Values
	body         []byte
	contentType  string
	output       string
}

func (c cli) parseCall(args []string) (request, error) {
	r := request{query: url.Values{}}
	var positional []string
	var fields [][2]string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-q", "-F", "-o":
			if i+1 >= len(args) {
				return r, usageError{arg + " needs a value"}
			}
			i++
			value := args[i]
			switch arg {
			case "-o":
				r.output = value
			default:
				name, text, ok := strings.Cut(value, "=")
				if !ok || name == "" {
					return r, usageError{arg + " takes name=value"}
				}
				if arg == "-q" {
					r.query.Add(name, text)
				} else {
					fields = append(fields, [2]string{name, text})
				}
			}
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) < 2 || len(positional) > 3 {
		return r, usageError{"call needs METHOD, PATH and optionally a JSON body"}
	}
	r.method, r.path = strings.ToUpper(positional[0]), positional[1]
	if !strings.HasPrefix(r.path, "/") {
		return r, usageError{"PATH starts with /, such as /v1/users/me"}
	}
	if len(positional) == 3 {
		if len(fields) > 0 {
			return r, usageError{"send either a JSON body or -F fields, not both"}
		}
		text := []byte(positional[2])
		if positional[2] == "-" {
			var err error
			if text, err = io.ReadAll(c.stdin); err != nil {
				return r, fmt.Errorf("cannot read the body from stdin: %w", err)
			}
		}
		if !json.Valid(text) {
			return r, usageError{"the body is not valid JSON"}
		}
		r.body, r.contentType = text, "application/json"
	}
	if len(fields) > 0 {
		var buffer bytes.Buffer
		form := multipart.NewWriter(&buffer)
		for _, field := range fields {
			name, value := field[0], field[1]
			path, isFile := strings.CutPrefix(value, "@")
			if !isFile {
				if err := form.WriteField(name, value); err != nil {
					return r, err
				}
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return r, fmt.Errorf("cannot read %s: %w", path, err)
			}
			part, err := form.CreateFormFile(name, filepath.Base(path))
			if err != nil {
				return r, err
			}
			if _, err = part.Write(data); err != nil {
				return r, err
			}
		}
		if err := form.Close(); err != nil {
			return r, err
		}
		r.body, r.contentType = buffer.Bytes(), form.FormDataContentType()
	}
	return r, nil
}

func (c cli) call(args []string) error {
	r, err := c.parseCall(args)
	if err != nil {
		return err
	}
	response, err := c.send(r)
	if err != nil {
		return err
	}
	return c.print(response, r.output)
}

// send makes the request with the credentials its operation takes, refreshing a kept session that has expired or that the server no longer accepts, once.
func (c cli) send(r request) (*http.Response, error) {
	if strings.HasPrefix(r.path, adminPrefix) {
		token := c.env("MSIME_ADMIN_TOKEN")
		if token == "" {
			return nil, errors.New("/api paths are the admin site's and need MSIME_ADMIN_TOKEN")
		}
		return c.do(c.adminServer(), r, token)
	}
	auth := authAny
	if operations, err := catalog(); err == nil {
		if op, ok := find(operations, r.method, r.path); ok {
			auth = op.Auth
		} else {
			fmt.Fprintf(c.stderr, "msime-cloud: %s %s is not in the API description; sending it anyway\n", r.method, r.path)
		}
	}
	server := c.server()
	if auth == authNone {
		return c.do(server, r, "")
	}
	if token := c.env("MSIME_CLOUD_TOKEN"); token != "" {
		return c.do(server, r, token)
	}
	sessions := c.store()
	current, ok, err := sessions.get(server)
	if err != nil {
		return nil, err
	}
	if !ok {
		response, err := c.do(server, r, "")
		if err == nil && response.StatusCode == http.StatusUnauthorized {
			fmt.Fprintln(c.stderr, "msime-cloud: not signed in; run `msime-cloud login start --email ...`, or set MSIME_CLOUD_TOKEN")
		}
		return response, err
	}
	refreshed := false
	if c.now().Add(refreshMargin).After(current.ExpiresAt) {
		if current, err = c.refresh(server, current); err != nil {
			return nil, err
		}
		refreshed = true
	}
	response, err := c.do(server, r, current.AccessToken)
	if err != nil || response.StatusCode != http.StatusUnauthorized || refreshed {
		return response, err
	}
	response.Body.Close()
	if current, err = c.refresh(server, current); err != nil {
		return nil, err
	}
	return c.do(server, r, current.AccessToken)
}

// refresh trades used's refresh token for a new pair, unless another process already did while this one waited for the lock.
func (c cli) refresh(server string, used session) (session, error) {
	sessions := c.store()
	unlock, err := sessions.lock()
	if err != nil {
		return session{}, err
	}
	defer unlock()
	latest, ok, err := sessions.get(server)
	if err != nil {
		return session{}, err
	}
	if !ok {
		return session{}, errors.New("signed out by another command; sign in again")
	}
	if latest.RefreshToken != used.RefreshToken {
		return latest, nil
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": latest.RefreshToken})
	response, err := c.do(server, request{method: "POST", path: "/v1/auth/refresh", body: body, contentType: "application/json"}, "")
	if err != nil {
		return session{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return session{}, err
	}
	if response.StatusCode == http.StatusUnauthorized {
		if err = sessions.put(server, nil); err != nil {
			return session{}, err
		}
		return session{}, errors.New("the session has ended; sign in again with `msime-cloud login start`")
	}
	if response.StatusCode != http.StatusOK {
		return session{}, fmt.Errorf("refreshing the session failed with %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	var tokens tokenResponse
	if err = json.Unmarshal(data, &tokens); err != nil || tokens.AccessToken == "" {
		return session{}, errors.New("the refresh response carries no token")
	}
	next := tokens.session(c.now())
	if len(next.User) == 0 {
		next.User = latest.User
	}
	return next, sessions.put(server, &next)
}

func (c cli) do(server string, r request, token string) (*http.Response, error) {
	target := strings.TrimRight(server, "/") + r.path
	if len(r.query) > 0 {
		separator := "?"
		if strings.Contains(target, "?") {
			separator = "&"
		}
		target += separator + r.query.Encode()
	}
	req, err := http.NewRequest(r.method, target, bytes.NewReader(r.body))
	if err != nil {
		return nil, usageError{err.Error()}
	}
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "msime-cloud")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.client.Do(req)
}

// print writes the body to output, or to stdout when it is JSON (indented) or text; a body of any other type needs a file.
func (c cli) print(response *http.Response, output string) error {
	defer response.Body.Close()
	failed := response.StatusCode < 200 || response.StatusCode > 299
	if output != "" && !failed {
		file, err := os.Create(output)
		if err != nil {
			return err
		}
		n, err := io.Copy(file, response.Body)
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		saved, _ := json.Marshal(map[string]any{"saved": output, "bytes": n, "content_type": response.Header.Get("Content-Type")})
		fmt.Fprintln(c.stdout, string(saved))
		return nil
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	kind, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	switch {
	case len(data) == 0:
	case kind == "application/json" || strings.HasSuffix(kind, "+json"):
		var indented bytes.Buffer
		if json.Indent(&indented, data, "", "  ") == nil {
			data = indented.Bytes()
		}
		fmt.Fprintln(c.stdout, strings.TrimRight(string(data), "\n"))
	case strings.HasPrefix(kind, "text/"):
		fmt.Fprintln(c.stdout, strings.TrimRight(string(data), "\n"))
	default:
		if !failed {
			return fmt.Errorf("the response is %s (%d bytes); pass -o <file> to save it", kind, len(data))
		}
	}
	if failed {
		return statusError{response.Status}
	}
	return nil
}

func (c cli) login(args []string) error {
	if len(args) == 0 {
		return usageError{"login needs start or finish"}
	}
	step, flags := args[0], map[string]string{}
	for i := 1; i < len(args); i += 2 {
		name := strings.TrimPrefix(args[i], "--")
		if name == args[i] || i+1 >= len(args) {
			return usageError{"login takes --name value pairs"}
		}
		flags[name] = args[i+1]
	}
	server := c.server()
	switch step {
	case "start":
		provider, target := "email", flags["email"]
		if phone := flags["phone"]; phone != "" {
			provider, target = "phone", phone
		}
		if len(flags) != 1 || target == "" {
			return usageError{"login start takes --email ADDRESS or --phone +8613800138000"}
		}
		body, _ := json.Marshal(map[string]string{"provider": provider, "target": target, "purpose": "login"})
		response, err := c.do(server, request{method: "POST", path: "/v1/auth/challenges", body: body, contentType: "application/json"}, "")
		if err != nil {
			return err
		}
		if err = c.print(response, ""); err == nil {
			fmt.Fprintf(c.stderr, "msime-cloud: a code was sent to %s; once the user reads it out, run `msime-cloud login finish --challenge <challenge_id> --code <code>`\n", target)
		}
		return err
	case "finish":
		challenge, code := flags["challenge"], flags["code"]
		if len(flags) != 2 || challenge == "" || code == "" {
			return usageError{"login finish takes --challenge ID --code CODE"}
		}
		body, _ := json.Marshal(map[string]string{"challenge_id": challenge, "credential": code})
		response, err := c.do(server, request{method: "POST", path: "/v1/auth/login", body: body, contentType: "application/json"}, "")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK {
			fmt.Fprintln(c.stdout, strings.TrimSpace(string(data)))
			return statusError{response.Status}
		}
		var tokens tokenResponse
		if err = json.Unmarshal(data, &tokens); err != nil || tokens.AccessToken == "" {
			return errors.New("the sign-in response carries no token")
		}
		signedIn := tokens.session(c.now())
		if err = c.store().put(server, &signedIn); err != nil {
			return err
		}
		// The tokens stay in the credentials file; only the user is shown.
		user, _ := json.MarshalIndent(json.RawMessage(tokens.User), "", "  ")
		fmt.Fprintln(c.stdout, string(user))
		return nil
	}
	return usageError{"login needs start or finish"}
}

func (c cli) logout(args []string) error {
	all := false
	switch {
	case len(args) == 1 && args[0] == "--all":
		all = true
	case len(args) != 0:
		return usageError{"logout takes only --all"}
	}
	server := c.server()
	sessions := c.store()
	if _, ok, err := sessions.get(server); err != nil || !ok {
		if err == nil {
			err = errors.New("not signed in to " + server)
		}
		return err
	}
	body, _ := json.Marshal(map[string]bool{"all": all})
	response, err := c.send(request{method: "POST", path: "/v1/auth/logout", body: body, contentType: "application/json"})
	if err != nil {
		return err
	}
	response.Body.Close()
	// A session the server already ended is as good as ended here.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusUnauthorized {
		return statusError{response.Status}
	}
	if err = sessions.put(server, nil); err != nil {
		return err
	}
	fmt.Fprintln(c.stderr, "msime-cloud: signed out of "+server)
	return nil
}

func (c cli) server() string {
	if value := c.env("MSIME_CLOUD_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return defaultServer
}

func (c cli) adminServer() string {
	if value := c.env("MSIME_ADMIN_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return defaultAdminServer
}

func (c cli) store() store {
	if dir := c.env("MSIME_CLOUD_CONFIG_DIR"); dir != "" {
		return store{dir: dir}
	}
	// Without a config directory the store refuses rather than leave tokens somewhere shared such as the temp directory.
	base, err := os.UserConfigDir()
	if err != nil {
		return store{}
	}
	return store{dir: filepath.Join(base, "msime-cloud")}
}
