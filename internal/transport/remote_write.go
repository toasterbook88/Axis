package transport

import (
	"encoding/base64"
	"fmt"

	"al.essio.dev/pkg/shellescape"
)

// BuildRemoteWriteCommand returns a shell-neutral command that writes content
// to path on a remote node. Base64 keeps payload bytes out of the login shell's
// parser, avoiding POSIX heredoc incompatibilities under shells such as fish.
func BuildRemoteWriteCommand(path string, content []byte) string {
	encoded := base64.StdEncoding.EncodeToString(content)
	quotedPath := shellescape.Quote(path)
	return fmt.Sprintf("mkdir -p $(dirname %s) && printf '%%s' %s | base64 -d > %s",
		quotedPath, shellescape.Quote(encoded), quotedPath)
}
