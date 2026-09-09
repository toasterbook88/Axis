package console

import (
	"strings"
	"unicode"
)

// Editor manages the interactive input buffer, cursor navigation, and command history ring.
type Editor struct {
	runes  []rune
	cursor int // rune index, 0 <= cursor <= len(runes)

	history      []string
	historyIndex int // -1 when on the active draft, 0..len(history)-1 when browsing history
	draft        string
}

// NewEditor creates an empty editor.
func NewEditor() Editor {
	return Editor{
		historyIndex: -1,
	}
}

// Text returns the editor contents as a string.
func (e *Editor) Text() string {
	return string(e.runes)
}

// Cursor returns the current cursor position as a rune offset.
func (e *Editor) Cursor() int {
	return e.cursor
}

// Runes returns a copy of the editor's rune buffer.
func (e *Editor) Runes() []rune {
	return append([]rune(nil), e.runes...)
}

// SetText replaces the current editor contents and places the cursor at the end.
func (e *Editor) SetText(s string) {
	e.runes = []rune(s)
	e.cursor = len(e.runes)
	e.historyIndex = -1
}

// Clear empties the editor buffer, resets cursor to 0, and restores normal draft state.
func (e *Editor) Clear() {
	e.runes = nil
	e.cursor = 0
	e.historyIndex = -1
	e.draft = ""
}

// Insert adds text at the current cursor position and advances the cursor.
func (e *Editor) Insert(s string) {
	if s == "" {
		return
	}
	newRunes := []rune(s)
	if e.cursor >= len(e.runes) {
		e.runes = append(e.runes, newRunes...)
		e.cursor = len(e.runes)
		return
	}
	tail := append([]rune(nil), e.runes[e.cursor:]...)
	e.runes = append(e.runes[:e.cursor], append(newRunes, tail...)...)
	e.cursor += len(newRunes)
}

// Backspace deletes the rune immediately preceding the cursor, if any.
func (e *Editor) Backspace() {
	if e.cursor <= 0 || len(e.runes) == 0 {
		return
	}
	e.runes = append(e.runes[:e.cursor-1], e.runes[e.cursor:]...)
	e.cursor--
}

// Delete removes the rune at the current cursor position, if any.
func (e *Editor) Delete() {
	if e.cursor < 0 || e.cursor >= len(e.runes) {
		return
	}
	e.runes = append(e.runes[:e.cursor], e.runes[e.cursor+1:]...)
}

// MoveLeft shifts the cursor one rune to the left, bounded by 0.
func (e *Editor) MoveLeft() {
	if e.cursor > 0 {
		e.cursor--
	}
}

// MoveRight shifts the cursor one rune to the right, bounded by len(runes).
func (e *Editor) MoveRight() {
	if e.cursor < len(e.runes) {
		e.cursor++
	}
}

// MoveHome positions the cursor at the beginning of the line.
func (e *Editor) MoveHome() {
	e.cursor = 0
}

// MoveEnd positions the cursor at the end of the line.
func (e *Editor) MoveEnd() {
	e.cursor = len(e.runes)
}

// DeleteToStart removes all characters from the beginning of the line up to the cursor.
func (e *Editor) DeleteToStart() {
	if e.cursor <= 0 {
		return
	}
	e.runes = append([]rune(nil), e.runes[e.cursor:]...)
	e.cursor = 0
}

// DeleteToEnd removes all characters from the cursor to the end of the line.
func (e *Editor) DeleteToEnd() {
	if e.cursor >= len(e.runes) {
		return
	}
	e.runes = append([]rune(nil), e.runes[:e.cursor]...)
}

// DeleteWordBefore deletes the word preceding the cursor, matching standard readline/bash behavior.
func (e *Editor) DeleteWordBefore() {
	if e.cursor <= 0 || len(e.runes) == 0 {
		return
	}
	idx := e.cursor
	// Skip trailing whitespace immediately before cursor
	for idx > 0 && unicode.IsSpace(e.runes[idx-1]) {
		idx--
	}
	// Delete non-whitespace characters
	for idx > 0 && !unicode.IsSpace(e.runes[idx-1]) {
		idx--
	}
	e.runes = append(e.runes[:idx], e.runes[e.cursor:]...)
	e.cursor = idx
}

// SetHistory replaces the history list.
func (e *Editor) SetHistory(history []string) {
	e.history = append([]string(nil), history...)
	e.historyIndex = -1
}

// History returns a copy of the command history.
func (e *Editor) History() []string {
	return append([]string(nil), e.history...)
}

// HistoryUp cycles to an older command in history. It saves the active draft on first press.
func (e *Editor) HistoryUp() {
	if len(e.history) == 0 {
		return
	}
	if e.historyIndex == -1 {
		e.draft = string(e.runes)
		e.historyIndex = len(e.history) - 1
	} else if e.historyIndex > 0 {
		e.historyIndex--
	} else {
		return
	}
	e.runes = []rune(e.history[e.historyIndex])
	e.cursor = len(e.runes)
}

// HistoryDown cycles to a newer command in history, restoring the uncommitted draft at the bottom.
func (e *Editor) HistoryDown() {
	if e.historyIndex == -1 {
		return
	}
	if e.historyIndex < len(e.history)-1 {
		e.historyIndex++
		e.runes = []rune(e.history[e.historyIndex])
		e.cursor = len(e.runes)
	} else {
		e.historyIndex = -1
		e.runes = []rune(e.draft)
		e.cursor = len(e.runes)
		e.draft = ""
	}
}

// Submit records non-empty input in the history ring and resets the editor for the next input.
func (e *Editor) Submit() string {
	text := string(e.runes)
	trimmed := strings.TrimSpace(text)
	if trimmed != "" {
		if len(e.history) == 0 || e.history[len(e.history)-1] != trimmed {
			e.history = append(e.history, trimmed)
		}
	}
	e.runes = nil
	e.cursor = 0
	e.historyIndex = -1
	e.draft = ""
	return text
}
