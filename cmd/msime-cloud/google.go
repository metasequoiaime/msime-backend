package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Google sign-in for a desktop: the RFC 8252 loopback flow the desktop app uses. The server holds the client secret and the PKCE verifier; this side listens on 127.0.0.1, checks the authorization URL the server built for it, and relays the code the browser brings back with the matching state.
const (
	googleSignInWait = 5 * time.Minute
	// Kept back from the challenge lifetime so a code that arrives at the end of the wait still finds the challenge alive.
	googleLoginMargin = 30 * time.Second
	googleCallback    = "/callback"
	callbackTimeout   = 2 * time.Second
)

const callbackPage = `<!doctype html><meta charset="utf-8"><title>水杉输入法</title><p>%s</p>`

func (c cli) googleSignIn(server string, open bool) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("cannot listen for the browser: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if port < 1024 {
		return fmt.Errorf("the system gave a privileged port %d for the browser to return to", port)
	}
	target := fmt.Sprintf("http://127.0.0.1:%d%s", port, googleCallback)
	body, _ := json.Marshal(map[string]string{"provider": "google", "target": target, "purpose": "login"})
	response, err := c.do(server, request{method: "POST", path: "/v1/auth/challenges", body: body, contentType: "application/json"}, "")
	if err != nil {
		return err
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		fmt.Fprintln(c.stdout, strings.TrimSpace(string(data)))
		return statusError{response.Status}
	}
	var challenge struct {
		ChallengeID      string `json:"challenge_id"`
		ExpiresIn        int    `json:"expires_in"`
		AuthorizationURL string `json:"authorization_url"`
	}
	if err = json.Unmarshal(data, &challenge); err != nil || challenge.ChallengeID == "" {
		return errors.New("the server answered the Google sign-in without a challenge")
	}
	state, err := googleState(challenge.AuthorizationURL, target)
	if err != nil {
		return err
	}
	wait := min(time.Duration(challenge.ExpiresIn)*time.Second-googleLoginMargin, googleSignInWait)
	if wait <= 0 {
		return errors.New("the sign-in challenge expires too soon to wait for the browser")
	}
	fmt.Fprintf(c.stderr, "msime-cloud: sign in with Google at this address; waiting %s for the browser to come back:\n%s\n", wait.Round(time.Second), challenge.AuthorizationURL)
	if open {
		if err = c.browse(challenge.AuthorizationURL); err != nil {
			fmt.Fprintf(c.stderr, "msime-cloud: cannot open a browser (%s); open the address yourself\n", err)
		}
	}
	code, err := receiveGoogleCode(listener, state, wait)
	if err != nil {
		return err
	}
	return c.signIn(server, challenge.ChallengeID, code)
}

// googleState checks that the address the server built is a plain Google authorization URL returning to target, and returns its state. The address is opened in a browser, so anything else is refused rather than opened.
func googleState(raw, target string) (string, error) {
	refused := errors.New("the server's Google sign-in address is not one this command opens")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "accounts.google.com" || parsed.User != nil || parsed.Fragment != "" || len(raw) > 4096 {
		return "", refused
	}
	query := parsed.Query()
	if len(query["redirect_uri"]) != 1 || query.Get("redirect_uri") != target || len(query["state"]) != 1 {
		return "", refused
	}
	state := query.Get("state")
	if state == "" || len(state) > 512 {
		return "", refused
	}
	return state, nil
}

// receiveGoogleCode answers requests on listener until the browser returns with state, then gives the authorization code. Requests for other paths or with another state are answered and ignored.
func receiveGoogleCode(listener net.Listener, state string, wait time.Duration) (string, error) {
	type outcome struct {
		code string
		err  error
	}
	done := make(chan outcome, 1)
	finish := func(result outcome) {
		select {
		case done <- result:
		default:
		}
	}
	page := func(w http.ResponseWriter, status int, text string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		fmt.Fprintf(w, callbackPage, text)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != googleCallback || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			page(w, http.StatusNotFound, "这不是本次登录的回调。")
			return
		}
		if query.Has("error") {
			page(w, http.StatusOK, "登录已取消，可以关闭此页面。")
			finish(outcome{err: errors.New("the Google sign-in was cancelled in the browser")})
			return
		}
		code := query.Get("code")
		if code == "" || len(code) > 2048 {
			page(w, http.StatusBadRequest, "登录失败，请回到终端重试。")
			finish(outcome{err: errors.New("the browser came back without a Google authorization code")})
			return
		}
		page(w, http.StatusOK, "登录完成，可以关闭此页面并回到终端。")
		finish(outcome{code: code})
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: callbackTimeout, ReadTimeout: callbackTimeout, WriteTimeout: callbackTimeout, MaxHeaderBytes: 8 << 10}
	go server.Serve(listener)
	defer server.Close()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.code, result.err
	case <-timer.C:
		return "", errors.New("the browser did not come back in time; run `msime-cloud login google` again")
	}
}

// openBrowser opens a URL googleState has already checked, so it holds nothing a shell or the opener could read as an option.
func openBrowser(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}
