package main

import (
	"fmt"
	"io"
	"strings"

	client "github.com/localitas/localitas-go"
)

// coreEnvCommand prints the environment that tells a process where this
// machine's core is and which token to act with (client.CoreEnv), one
// KEY=VALUE per line, as `docker run --env-file` reads it: --for native
// (the default) gives core's own URL, --for container the URL a container
// on this machine reaches it at (client.ContainerURL), as core gives the
// containers it starts.
func coreEnvCommand(args []string, out io.Writer) error {
	vantage := "native"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--for" && i+1 < len(args):
			vantage = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--for="):
			vantage = strings.TrimPrefix(args[i], "--for=")
		default:
			return fmt.Errorf("unknown argument %q (usage: localitas core-env [--for native|container])", args[i])
		}
	}
	coreURL, token, err := coreAddress(configPath)
	if err != nil {
		return err
	}
	switch vantage {
	case "native":
	case "container":
		coreURL = client.ContainerURL(coreURL)
	default:
		return fmt.Errorf("--for %q: want native or container", vantage)
	}
	for _, kv := range client.CoreEnv(coreURL, token) {
		fmt.Fprintln(out, kv)
	}
	return nil
}
