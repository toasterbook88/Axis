package agent

import (
	"io"
	"strings"
)

// ColorWriter statefully highlights code blocks (bold cyan) and inline code (yellow)
// in streamed agent output.
type ColorWriter struct {
	w           io.Writer
	inCodeBlock bool
	inInline    bool
	buf         string
}

// NewColorWriter wraps an io.Writer.
func NewColorWriter(w io.Writer) *ColorWriter {
	return &ColorWriter{w: w}
}

func (cw *ColorWriter) Write(p []byte) (n int, err error) {
	cw.buf += string(p)

	for {
		if len(cw.buf) == 0 {
			break
		}

		// If buffer ends with backticks and has length < 3, wait for more data.
		if (strings.HasSuffix(cw.buf, "`") || strings.HasSuffix(cw.buf, "``")) && len(cw.buf) < 3 {
			break
		}

		if strings.HasPrefix(cw.buf, "```") {
			if cw.inCodeBlock {
				// Exit code block: reset colors
				_, err = io.WriteString(cw.w, "```\033[0m")
				cw.inCodeBlock = false
			} else {
				// Enter code block: print backticks and switch to bold cyan
				_, err = io.WriteString(cw.w, "```\033[36;1m")
				cw.inCodeBlock = true
			}
			if err != nil {
				return len(p), err
			}
			cw.buf = cw.buf[3:]
			continue
		}

		if strings.HasPrefix(cw.buf, "`") {
			if !cw.inCodeBlock {
				if cw.inInline {
					// Exit inline code
					_, err = io.WriteString(cw.w, "`\033[0m")
					cw.inInline = false
				} else {
					// Enter inline code (yellow)
					_, err = io.WriteString(cw.w, "`\033[33m")
					cw.inInline = true
				}
				if err != nil {
					return len(p), err
				}
				cw.buf = cw.buf[1:]
				continue
			}
		}

		// Write the first character
		_, err = cw.w.Write([]byte{cw.buf[0]})
		cw.buf = cw.buf[1:]
		if err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// Close flushes any remaining buffered text and resets styles.
func (cw *ColorWriter) Close() error {
	if len(cw.buf) > 0 {
		if _, err := cw.w.Write([]byte(cw.buf)); err != nil {
			cw.buf = ""
			return err
		}
		cw.buf = ""
	}
	if cw.inCodeBlock || cw.inInline {
		cw.inCodeBlock = false
		cw.inInline = false
		if _, err := io.WriteString(cw.w, "\033[0m"); err != nil {
			return err
		}
	}
	return nil
}
