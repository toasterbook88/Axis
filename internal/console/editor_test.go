package console

import (
	"reflect"
	"testing"
)

func TestEditorBasicEditing(t *testing.T) {
	ed := NewEditor()

	ed.Insert("hello")
	if ed.Text() != "hello" || ed.Cursor() != 5 {
		t.Fatalf("after insert: text=%q, cursor=%d; want hello, 5", ed.Text(), ed.Cursor())
	}

	ed.MoveLeft()
	ed.MoveLeft()
	if ed.Cursor() != 3 {
		t.Fatalf("after move left: cursor=%d; want 3", ed.Cursor())
	}

	ed.Insert("X")
	if ed.Text() != "helXlo" || ed.Cursor() != 4 {
		t.Fatalf("after insert X: text=%q, cursor=%d; want helXlo, 4", ed.Text(), ed.Cursor())
	}

	ed.Backspace()
	if ed.Text() != "hello" || ed.Cursor() != 3 {
		t.Fatalf("after backspace: text=%q, cursor=%d; want hello, 3", ed.Text(), ed.Cursor())
	}

	ed.Delete()
	if ed.Text() != "helo" || ed.Cursor() != 3 {
		t.Fatalf("after delete: text=%q, cursor=%d; want helo, 3", ed.Text(), ed.Cursor())
	}

	ed.MoveHome()
	if ed.Cursor() != 0 {
		t.Fatalf("after home: cursor=%d; want 0", ed.Cursor())
	}

	ed.MoveEnd()
	if ed.Cursor() != 4 {
		t.Fatalf("after end: cursor=%d; want 4", ed.Cursor())
	}
}

func TestEditorWordAndLineDeletions(t *testing.T) {
	ed := NewEditor()
	ed.SetText("axis agent status")

	// Delete word before cursor at the end
	ed.DeleteWordBefore()
	if ed.Text() != "axis agent " {
		t.Fatalf("after DeleteWordBefore: text=%q, want 'axis agent '", ed.Text())
	}

	ed.DeleteWordBefore()
	if ed.Text() != "axis " {
		t.Fatalf("after second DeleteWordBefore: text=%q, want 'axis '", ed.Text())
	}

	ed.SetText("foo bar baz")
	ed.MoveHome()
	ed.MoveRight() // on 'o'
	ed.MoveRight() // on 'o'
	ed.DeleteToStart()
	if ed.Text() != "o bar baz" || ed.Cursor() != 0 {
		t.Fatalf("after DeleteToStart: text=%q, cursor=%d; want 'o bar baz', 0", ed.Text(), ed.Cursor())
	}

	ed.SetText("foo bar baz")
	ed.MoveHome()
	ed.MoveRight()
	ed.MoveRight()
	ed.MoveRight() // after 'foo'
	ed.DeleteToEnd()
	if ed.Text() != "foo" || ed.Cursor() != 3 {
		t.Fatalf("after DeleteToEnd: text=%q, cursor=%d; want 'foo', 3", ed.Text(), ed.Cursor())
	}
}

func TestEditorHistoryRing(t *testing.T) {
	ed := NewEditor()

	ed.SetText("cmd 1")
	ed.Submit()

	ed.SetText("cmd 2")
	ed.Submit()

	// Submitting duplicate does not double-record
	ed.SetText("cmd 2")
	ed.Submit()

	wantHist := []string{"cmd 1", "cmd 2"}
	if !reflect.DeepEqual(ed.History(), wantHist) {
		t.Fatalf("history = %v; want %v", ed.History(), wantHist)
	}

	// Draft in progress
	ed.SetText("my draft")

	// Up brings cmd 2
	ed.HistoryUp()
	if ed.Text() != "cmd 2" {
		t.Fatalf("history up 1: text=%q, want 'cmd 2'", ed.Text())
	}

	// Up brings cmd 1
	ed.HistoryUp()
	if ed.Text() != "cmd 1" {
		t.Fatalf("history up 2: text=%q, want 'cmd 1'", ed.Text())
	}

	// Up at top is clamped
	ed.HistoryUp()
	if ed.Text() != "cmd 1" {
		t.Fatalf("history up clamped: text=%q, want 'cmd 1'", ed.Text())
	}

	// Down brings cmd 2
	ed.HistoryDown()
	if ed.Text() != "cmd 2" {
		t.Fatalf("history down 1: text=%q, want 'cmd 2'", ed.Text())
	}

	// Down restores original draft
	ed.HistoryDown()
	if ed.Text() != "my draft" {
		t.Fatalf("history down to draft: text=%q, want 'my draft'", ed.Text())
	}

	// Down again is clamped
	ed.HistoryDown()
	if ed.Text() != "my draft" {
		t.Fatalf("history down clamped: text=%q, want 'my draft'", ed.Text())
	}
}
