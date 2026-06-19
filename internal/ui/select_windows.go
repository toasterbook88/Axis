//go:build windows

package ui

import (
	"io"
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
	if b == 3 {
		return ActionCancel, nil
	}
	if b == 13 || b == 10 {
		return ActionEnter, nil
	}

	if b == 27 {
		return ActionCancel, nil
	}

	return ActionNone, nil
}
