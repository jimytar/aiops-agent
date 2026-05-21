package bot

import (
	"encoding/json"
	"sync"
)

const maxHistoryMessages = 40

// sessionStore holds per-chat conversation history.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[int64]*session
}

type session struct {
	messages []json.RawMessage
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		sessions: make(map[int64]*session),
	}
}

func (s *sessionStore) get(chatID int64) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[chatID]
	if !ok {
		sess = &session{}
		s.sessions[chatID] = sess
	}
	return sess
}

func (s *sessionStore) reset(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[chatID] = &session{}
}

func (sess *session) append(msg json.RawMessage) {
	sess.messages = append(sess.messages, msg)
	if len(sess.messages) > maxHistoryMessages {
		sess.messages = sess.messages[len(sess.messages)-maxHistoryMessages:]
	}
}

func (sess *session) history() []json.RawMessage {
	cp := make([]json.RawMessage, len(sess.messages))
	copy(cp, sess.messages)
	return cp
}
