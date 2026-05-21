package bot

import (
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func makeMsg(role string) json.RawMessage {
	var msg anthropic.MessageParam
	if role == "user" {
		msg = anthropic.NewUserMessage(anthropic.NewTextBlock("hello"))
	} else {
		msg = anthropic.NewAssistantMessage(anthropic.NewTextBlock("hi"))
	}
	raw, _ := json.Marshal(msg)
	return raw
}

func TestSessionStoreGetCreatesNew(t *testing.T) {
	s := newSessionStore()
	sess := s.get(42)
	if sess == nil {
		t.Fatal("get should return non-nil session")
	}
	if len(sess.messages) != 0 {
		t.Errorf("new session should have 0 messages, got %d", len(sess.messages))
	}
}

func TestSessionStoreGetSameInstance(t *testing.T) {
	s := newSessionStore()
	a := s.get(1)
	b := s.get(1)
	if a != b {
		t.Error("same chatID should return same session instance")
	}
}

func TestSessionStoreDifferentChats(t *testing.T) {
	s := newSessionStore()
	a := s.get(1)
	b := s.get(2)
	if a == b {
		t.Error("different chatIDs should return different sessions")
	}
}

func TestSessionStoreReset(t *testing.T) {
	s := newSessionStore()
	sess := s.get(10)
	sess.append(makeMsg("user"))

	s.reset(10)
	fresh := s.get(10)
	if len(fresh.messages) != 0 {
		t.Errorf("reset session should have 0 messages, got %d", len(fresh.messages))
	}
}

func TestSessionAppend(t *testing.T) {
	sess := &session{}
	sess.append(makeMsg("user"))
	sess.append(makeMsg("assistant"))
	if len(sess.messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(sess.messages))
	}
}

func TestSessionAppendTrimsAtMax(t *testing.T) {
	sess := &session{}
	for i := 0; i <= maxHistoryMessages; i++ {
		sess.append(makeMsg("user"))
	}
	if len(sess.messages) != maxHistoryMessages {
		t.Errorf("expected %d messages after trim, got %d", maxHistoryMessages, len(sess.messages))
	}
}

func TestSessionAppendKeepsLast(t *testing.T) {
	sess := &session{}
	for i := 0; i < maxHistoryMessages+5; i++ {
		sess.append(makeMsg("user"))
	}
	if len(sess.messages) != maxHistoryMessages {
		t.Errorf("expected %d messages, got %d", maxHistoryMessages, len(sess.messages))
	}
}

func TestSessionHistory(t *testing.T) {
	sess := &session{}
	sess.append(makeMsg("user"))
	sess.append(makeMsg("assistant"))

	h := sess.history()
	if len(h) != 2 {
		t.Errorf("history len = %d", len(h))
	}
}

func TestSessionHistoryIsCopy(t *testing.T) {
	sess := &session{}
	sess.append(makeMsg("user"))

	h := sess.history()
	h = append(h, makeMsg("assistant"))

	// Original session should be unaffected.
	if len(sess.messages) != 1 {
		t.Errorf("modifying history slice should not affect session; got %d messages", len(sess.messages))
	}
}
