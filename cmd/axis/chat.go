package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/chat"
)

func chatCmd() *cobra.Command {
	var model string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "chat [message...]",
		Short: "Natural language interface to AXIS",
		Long:  "Chat with the AXIS cluster intelligence using a local LLM or fallback interface.",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			engine := chat.NewEngine(model)

			if len(args) > 0 {
				ctx, cancel := chatRequestContext(timeout)
				defer cancel()
				query := strings.Join(args, " ")
				fmt.Printf("AXIS [Model: %s] | Thinking...\n\n", model)

				if err := engine.GenerateStream(ctx, query, os.Stdout); err != nil {
					Fatal(ExitErrCommandFail, "Chat engine failed: %v", err)
				}
				fmt.Println()
				return nil
			}

			fmt.Printf("AXIS Chat Session [Model: %s]\nType 'exit' or 'quit' to leave.\n\n", model)
			scanner := bufio.NewScanner(os.Stdin)
			var history string
			for {
				fmt.Print(">>> ")
				if !scanner.Scan() {
					break
				}
				query := strings.TrimSpace(scanner.Text())
				if query == "" {
					continue
				}
				if strings.ToLower(query) == "exit" || strings.ToLower(query) == "quit" {
					break
				}

				prompt := query
				if history != "" {
					prompt = history + "\nUser: " + query + "\nAssistant: "
				}

				var buf bytes.Buffer
				w := io.MultiWriter(os.Stdout, &buf)

				ctx, cancel := chatRequestContext(timeout)
				if err := engine.GenerateStream(ctx, prompt, w); err != nil {
					fmt.Printf("\n[Error: %v]\n", err)
				}
				cancel()
				fmt.Println()

				if history != "" {
					history = history + "\nUser: " + query + "\nAssistant: " + buf.String()
				} else {
					history = "User: " + query + "\nAssistant: " + buf.String()
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&model, "model", "m", "qwen2.5-coder:1.5b", "Ollama model to use for inference")
	cmd.Flags().DurationVarP(&timeout, "timeout", "t", 2*time.Minute, "Per-request timeout for chat generation (0 disables timeout)")
	return cmd
}

func chatRequestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), timeout)
}
