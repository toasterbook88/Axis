package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/chat"
)

func chatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat [message...]",
		Short: "Natural language interface to AXIS",
		Long:  "Chat with the AXIS cluster intelligence using a local LLM or fallback interface.",
		Args:  cobra.ArbitraryArgs,
		RunE:  runChat,
	}

	cmd.Flags().StringP("model", "m", "llama3", "Ollama model to use for inference")
	return cmd
}

func runChat(cmd *cobra.Command, args []string) error {
	model, _ := cmd.Flags().GetString("model")
	ctx := context.Background()
	engine := chat.NewEngine(model)

	if len(args) > 0 {
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

		if err := engine.GenerateStream(ctx, prompt, w); err != nil {
			fmt.Printf("\n[Error: %v]\n", err)
		}
		fmt.Println("\n")

		if history != "" {
			history = history + "\nUser: " + query + "\nAssistant: " + buf.String()
		} else {
			history = "User: " + query + "\nAssistant: " + buf.String()
		}
	}
	return nil
}
