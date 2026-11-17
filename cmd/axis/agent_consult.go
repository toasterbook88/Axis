package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

// consultModelResolver is the resolver the agent command installs.
// The choice list is the same one /model builds. A failed lookup becomes a
// consult error that quotes the caller's seat and does not tell the driver
// to pull a model. BuildBackend runs only after the ref resolves.
func consultModelResolver(loadRT func(context.Context) (*runtimectx.Context, error)) agent.ConsultResolver {
	return consultResolver(func(ctx context.Context) []ModelChoice {
		if loadRT == nil {
			return nil
		}
		rt, err := loadRT(ctx)
		if err != nil || rt == nil {
			return nil
		}
		return collectModelChoices(rt)
	}, loadRT, agent.BuildBackend)
}

func consultResolver(
	list func(context.Context) []ModelChoice,
	loadRT func(context.Context) (*runtimectx.Context, error),
	build func(agent.ModelTarget, agent.CloudBackendOptions) (agent.ChatBackend, error),
) agent.ConsultResolver {
	if build == nil {
		build = agent.BuildBackend
	}
	return func(ctx context.Context, seatRef string) (agent.ChatBackend, string, func(), error) {
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		quoted := strings.TrimSpace(seatRef)
		var choices []ModelChoice
		if list != nil {
			choices = list(ctx)
		}
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		choice, err := findModelTargetByRef(choices, quoted)
		if err != nil {
			return nil, "", nil, consultResolveError(quoted, err)
		}
		// A catalog endpoint is the node's observed address and may not be
		// reachable from here. Resolve a working route (listed address, node
		// name, SSH tunnel) before building the seat. The tunnel is returned
		// to the caller, which closes it when the consult ends. It never
		// replaces the driver's tunnel.
		var rt *runtimectx.Context
		if loadRT != nil {
			rt, _ = loadRT(ctx)
		}
		if err := ctx.Err(); err != nil {
			return nil, "", nil, err
		}
		routed, tunnel, err := resolveModelRoute(ctx, rt, choice)
		if err != nil {
			return nil, "", nil, consultDetail(quoted, err)
		}
		opts, err := cloudOptsForTarget(ctx, loadRT, &routed)
		if err != nil {
			if tunnel != nil {
				tunnel()
			}
			return nil, "", nil, consultDetail(quoted, err)
		}
		backend, err := build(routed, opts)
		if err != nil {
			if tunnel != nil {
				tunnel()
			}
			return nil, "", nil, consultDetail(quoted, err)
		}
		if backend == nil || strings.TrimSpace(routed.ID) == "" {
			if tunnel != nil {
				tunnel()
			}
			return nil, "", nil, fmt.Errorf("consult: seat %q is not answering", quoted)
		}
		if c, ok := backend.(*chat.Client); ok {
			c.DisableAutoStart()
		}
		return backend, routed.ID, tunnel, nil
	}
}

// consultResolveError maps findModelTargetByRef. An ambiguous name may list
// the matching ids. Every other failure stays a consult error that quotes
// the caller's seat and drops the pull instruction.
func consultResolveError(seat string, err error) error {
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "is ambiguous") && !strings.Contains(strings.ToLower(msg), "pull") {
			return fmt.Errorf("consult: seat %q is not answering: %s", seat, msg)
		}
	}
	return fmt.Errorf("consult: seat %q is not answering", seat)
}

func consultDetail(seat string, err error) error {
	if err == nil || strings.Contains(strings.ToLower(err.Error()), "pull") {
		return fmt.Errorf("consult: seat %q is not answering", seat)
	}
	return fmt.Errorf("consult: seat %q is not answering: %w", seat, err)
}
