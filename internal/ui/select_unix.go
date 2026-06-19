//go:build !windows

package ui

import (
	"io"
	"time"
)

func readKey(in io.Reader, fd int, isTTY bool) (KeyAction, error) {
	if !isTTY {
		return ActionNone, nil
	}

	var buf [1]byte
	n, err := in.Read(buf[:])
	if err != nil {
		return ActionNone, err
	}
	if n == 0 {
		return ActionNone, nil
	}

	b := buf[0]
	if b == 3 { // Ctrl+C
		return ActionCancel, nil
	}
	if b == 13 || b == 10 { // Enter
		return ActionEnter, nil
	}

	if b == 27 { // ESC
		hasMore, err := isInputAvailable(fd, 50*time.Millisecond)
		if err != nil || !hasMore {
			return ActionCancel, nil
		}

		n, err = in.Read(buf[:])
		if err != nil || n == 0 {
			return ActionCancel, nil
		}
		if buf[0] != '[' {
			return ActionCancel, nil
		}

		hasMore, err = isInputAvailable(fd, 50*time.Millisecond)
		if err != nil || !hasMore {
			return ActionCancel, nil
		}
		n, err = in.Read(buf[:])
		if err != nil || n == 0 {
			return ActionCancel, nil
		}
		switch buf[0] {
		case 'A':
			return ActionUp, nil
		case 'B':
			return ActionDown, nil
		}
		return ActionNone, nil
	}

	return ActionNone, nil
}
