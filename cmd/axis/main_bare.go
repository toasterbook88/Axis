package main

import (
	"context"
	"errors"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// isInteractiveFn reports whether both stdin and stdout are terminals. It is a
// var so tests can drive the front-door decision without a real TTY.
var isInteractiveFn = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

// runBareAxis runs `axis agent` exactly as if invoked with no flags or args.
//
// It calls the agent command's RunE directly rather than re-executing the
// root: re-execution would re-run PersistentPreRunE (cortex client, webhook
// registration) a second time. The agent command has no PreRunE of its own,
// and flags are bound to their defaults at construction, so an unparsed
// command carries default flag values. Cobra only sets a context on the
// command it dispatches to, so it is propagated explicitly; the agent's
// signal handling derives from cmd.Context().
var runBareAxis = func(cmd *cobra.Command) error {
	agent, _, err := cmd.Find([]string{"agent"})
	if err != nil || agent == nil || agent.Name() != "agent" || agent.RunE == nil {
		return errors.New("agent command is not registered")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	agent.SetContext(ctx)
	return agent.RunE(agent, nil)
}
