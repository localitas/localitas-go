package client

import (
	"bufio"
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// How a process learns where core is and which token it acts with. Core
// gives an app it starts (a container, a FaaS function) the environment
// CoreEnv builds; for the other processes of its machine (natively run
// apps, tools) each node writes the same variables to CoreEnvFile in its
// home, with its own loopback URL. Default reads the environment first and
// the home's file second.
const (
	// CoreURLVar is core's base URL as the process reaches it.
	CoreURLVar = "CORE_URL"
	// TokenVar is the API token the process acts with.
	TokenVar = "LOCALITAS_API_TOKEN"
	// HomeVar names the home of the core a process belongs to, for a
	// machine with more than one core (a dev cluster); unset, it is
	// ~/.localitas, where core keeps its config by default.
	HomeVar = "LOCALITAS_HOME"
	// CoreEnvFile is the file in a core's home that holds CoreEnv for the
	// processes of its machine.
	CoreEnvFile = "core.env"
)

// CoreEnv is the environment that tells a process where core is and which
// token to act with, as KEY=VALUE entries; an empty value is left out.
func CoreEnv(coreURL, token string) []string {
	var env []string
	if coreURL != "" {
		env = append(env, CoreURLVar+"="+coreURL)
	}
	if token != "" {
		env = append(env, TokenVar+"="+token)
	}
	return env
}

// Home is the home of the core this process belongs to: $LOCALITAS_HOME,
// else ~/.localitas ("" when there is no user home).
func Home() string {
	if h := os.Getenv(HomeVar); h != "" {
		return h
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(user, ".localitas")
}

// WriteCoreEnv writes CoreEnv(coreURL, token) to CoreEnvFile in home,
// private to its owner as it holds the token. It replaces the file whole,
// so a reader never sees half of it.
func WriteCoreEnv(home, coreURL, token string) error {
	var b bytes.Buffer
	b.WriteString("# Written by localitas-core each time it serves: how a process of this\n")
	b.WriteString("# machine reaches it. It holds an API token: keep it private.\n")
	for _, kv := range CoreEnv(coreURL, token) {
		b.WriteString(kv + "\n")
	}
	tmp, err := os.CreateTemp(home, "."+CoreEnvFile+"-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", CoreEnvFile, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", CoreEnvFile, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", CoreEnvFile, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(home, CoreEnvFile)); err != nil {
		return fmt.Errorf("write %s: %w", CoreEnvFile, err)
	}
	return nil
}

// CoreAt is the URL and token the core whose home is home wrote in its
// CoreEnvFile, for a tool told which core to reach (its config file's
// directory). It fails, naming the file, when the file is missing or its
// URL is not an http(s) URL; the token may be empty.
func CoreAt(home string) (coreURL, token string, err error) {
	s := coreSettings{file: filepath.Join(home, CoreEnvFile)}
	s.readFile()
	if s.fileErr != nil {
		return "", "", fmt.Errorf("%w: %v", ErrNoCoreURL, s.fileErr)
	}
	u, err := s.checkedURL()
	if err != nil {
		return "", "", err
	}
	return u, s.token, nil
}

// coreSettings is where a process finds core: each variable from the
// environment when set, else from file.
type coreSettings struct {
	url, token string
	urlFrom    string
	file       string
	fileErr    error
}

// resolveCore reads CORE_URL and LOCALITAS_API_TOKEN from the environment,
// and what is missing from the CoreEnvFile of Home().
func resolveCore() coreSettings {
	s := coreSettings{url: os.Getenv(CoreURLVar), token: os.Getenv(TokenVar), urlFrom: "environment"}
	s.file = filepath.Join(Home(), CoreEnvFile)
	if s.url == "" || s.token == "" {
		s.readFile()
	}
	return s
}

// readFile fills what s lacks from s.file.
func (s *coreSettings) readFile() {
	data, err := os.ReadFile(s.file)
	if err != nil {
		s.fileErr = err
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case CoreURLVar:
			if s.url == "" {
				s.url, s.urlFrom = strings.TrimSpace(value), s.file
			}
		case TokenVar:
			if s.token == "" {
				s.token = strings.TrimSpace(value)
			}
		}
	}
}

// checkedURL is s.url checked to be core's http(s) URL; the error names
// where the URL came from, or the variable and the file when there is none.
func (s coreSettings) checkedURL() (string, error) {
	if s.url == "" {
		return "", fmt.Errorf("%w (%s has none)", ErrNoCoreURL, s.file)
	}
	u, err := url.Parse(s.url)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%s %q (from the %s) is not an http(s) URL of core", CoreURLVar, s.url, s.urlFrom)
	}
	return s.url, nil
}
