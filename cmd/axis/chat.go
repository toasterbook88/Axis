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
cmd := &cobra.Command{
Use:   "chat [message...]",
Short: "Natural language interface to AXIS",
Long:  "Chat with the AXIS cluster intelligence using a local LLM or fallback interface.",
Args:  cobra.ArbitraryArgs,
RunE:  runChat,
}

cmd.Flags().StringP("model", "m", "", "Ollama model to use for inference (default: best installed recommended model)")
cmd.Flags().DurationP("timeout", "t", 2*time.Minute, "Per-request timeout for chat generation (0 disables timeout)")
return cmd
}

func runChat(cmd *cobra.Command, args []string) error {
model, _ := cmd.Flags().GetString("model")
timeout, _ := cmd.Flags().GetDuration("timeout")
currentModel := resolveChatModel(model)

if len(args) > 0 {
if handled, nextModel := handleChatMetaCommand(strings.Join(args, " "), currentModel); handled {
if nextModel == "" {
return nil
}
currentModel = nextModel
return nil
}

engine := chat.NewEngine(currentModel)
ctx, cancel := chatRequestContext(timeout)
defer cancel()
query := strings.Join(args, " ")
fmt.Printf("AXIS [Model: %s] | Thinking...\n\n", currentModel)

if err := engine.GenerateStream(ctx, query, os.Stdout); err != nil {
Fatal(ExitErrCommandFail, "Chat engine failed: %v", err)
}
fmt.Println()
return nil
}

fmt.Printf("AXIS Chat Session [Model: %s]\nType 'exit' or 'quit' to leave. Use '/models' to browse model options.\n\n", currentModel)
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
if handled, nextModel := handleChatMetaCommand(query, currentModel); handled {
if nextModel != "" {
currentModel = nextModel
fmt.Printf("Switched model to %s\n\n", currentModel)
}
continue
}

engine := chat.NewEngine(currentModel)

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
}

func chatRequestContext(timeout time.Duration) (context.Context, context.CancelFunc) {
if timeout <= 0 {
return context.WithCancel(context.Background())
}
return context.WithTimeout(context.Background(), timeout)
}

func resolveChatModel(requested string) string {
if strings.TrimSpace(requested) != "" {
return strings.TrimSpace(requested)
}

ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
defer cancel()
return chat.ResolveDefaultModel(ctx)
}

func handleChatMetaCommand(input, currentModel string) (handled bool, nextModel string) {
query := strings.TrimSpace(input)
switch {
case query == "/models":
ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
defer cancel()
fmt.Println(chat.FormatModelCatalog(chat.BuildModelCatalog(ctx, currentModel)))
return true, ""
case strings.HasPrefix(query, "/model "):
next := strings.TrimSpace(strings.TrimPrefix(query, "/model "))
if next == "" {
fmt.Println("Usage: /model <tag>")
return true, ""
}
return true, next
case query == "/model":
fmt.Println("Usage: /model <tag>")
return true, ""
default:
return false, ""
}
}
