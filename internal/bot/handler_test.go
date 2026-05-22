package bot

import (
	"strings"
	"testing"

	"github.com/jimytar/aiops-agent/internal/config"
)

func newHandlerForTest(allowedIDs []int64) *Handler {
	cfg := &config.Config{AllowedChatIDs: allowedIDs}
	return &Handler{cfg: cfg}
}

func TestIsAuthorizedAllowed(t *testing.T) {
	h := newHandlerForTest([]int64{111, 222, 333})
	for _, id := range []int64{111, 222, 333} {
		if !h.isAuthorized(id) {
			t.Errorf("isAuthorized(%d) = false, want true", id)
		}
	}
}

func TestIsAuthorizedDenied(t *testing.T) {
	h := newHandlerForTest([]int64{111})
	for _, id := range []int64{0, 999, -1} {
		if h.isAuthorized(id) {
			t.Errorf("isAuthorized(%d) = true, want false", id)
		}
	}
}

func TestIsAuthorizedEmptyList(t *testing.T) {
	h := newHandlerForTest(nil)
	if h.isAuthorized(42) {
		t.Error("isAuthorized should return false when allowedChatIDs is empty")
	}
}

func TestWelcomeMessageContainsCommands(t *testing.T) {
	msg := welcomeMessage()
	for _, cmd := range []string{"/status", "/reset", "/cancel"} {
		if !strings.Contains(msg, cmd) {
			t.Errorf("welcome message missing %q", cmd)
		}
	}
}
