package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/chat"
)

func chatCmd() *cobra.Command {
	var model string

	cmd := &cobra.Command{
		Use:   "chat [message...]",
		Short: "Natural language interface to AXIS",
		Long:  "Chat with the AXIS cluster intelligence using a local LLM or fallback interface.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			
			ctx := context.Background()
			engine := chat.NewEngine(model)
			
			fmt.Printf("AXIS [Model: %s] | Thinking...\n\n", model)
			
			if err := engine.GenerateStream(ctx, query, os.Stdout); err != nil {
				Fatal(ExitErrCommandFail, "Chat engine failed: %v", err)
			}
			
			fmt.Println()
			return nil
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "llama3", "Ollama model to use for inference")
	return cmd
}
