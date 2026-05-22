package executor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newFrigateServer(mux *http.ServeMux) (*httptest.Server, *FrigateExecutor) {
	srv := httptest.NewServer(mux)
	return srv, NewFrigateExecutor(srv.URL)
}

// --- Cameras ---

func TestFrigateCameras(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"cameras": map[string]interface{}{
				"front_door": map[string]interface{}{},
				"backyard":   map[string]interface{}{},
			},
		})
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	result, err := exec.Cameras()
	if err != nil {
		t.Fatalf("Cameras() error: %v", err)
	}
	if !strings.Contains(result, "front_door") {
		t.Errorf("expected front_door in result, got: %s", result)
	}
	if !strings.Contains(result, "backyard") {
		t.Errorf("expected backyard in result, got: %s", result)
	}
}

func TestFrigateCamerasEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"cameras": map[string]interface{}{},
		})
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	result, err := exec.Cameras()
	if err != nil {
		t.Fatalf("Cameras() error: %v", err)
	}
	if !strings.Contains(result, "No cameras") {
		t.Errorf("expected 'No cameras' message, got: %s", result)
	}
}

func TestFrigateCamerasServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	_, err := exec.Cameras()
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

// --- Snapshot ---

func TestFrigateSnapshotSuccess(t *testing.T) {
	fakeJPEG := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x01, 0x02, 0x03} // fake JPEG header
	mux := http.NewServeMux()
	mux.HandleFunc("/api/front_door/latest.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(fakeJPEG)
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	data, mediaType, err := exec.Snapshot("front_door")
	if err != nil {
		t.Fatalf("Snapshot() error: %v", err)
	}
	if mediaType != "image/jpeg" {
		t.Errorf("mediaType = %q, want image/jpeg", mediaType)
	}
	if len(data) != len(fakeJPEG) {
		t.Errorf("expected %d bytes, got %d", len(fakeJPEG), len(data))
	}
}

func TestFrigateSnapshotNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/unknown/latest.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	_, _, err := exec.Snapshot("unknown")
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

func TestFrigateSnapshotURLConstructed(t *testing.T) {
	var gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/cam1/latest.jpg", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte("jpeg"))
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	exec.Snapshot("cam1")
	if gotQuery != "h=720" {
		t.Errorf("expected ?h=720 query param, got %q", gotQuery)
	}
}

// --- Events ---

func TestFrigateEvents(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("label") != "person" {
			t.Errorf("expected label=person, got %q", r.URL.Query().Get("label"))
		}
		if r.URL.Query().Get("limit") != "5" {
			t.Errorf("expected limit=5, got %q", r.URL.Query().Get("limit"))
		}
		json.NewEncoder(w).Encode([]frigateEvent{
			{ID: "1", Camera: "front_door", Label: "person", Score: 0.92, StartTime: 1700000000},
			{ID: "2", Camera: "backyard", Label: "person", Score: 0.85, StartTime: 1700000060, HasClip: true},
		})
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	result, err := exec.Events("", "person", 5)
	if err != nil {
		t.Fatalf("Events() error: %v", err)
	}
	if !strings.Contains(result, "front_door") {
		t.Errorf("expected front_door in result, got: %s", result)
	}
	if !strings.Contains(result, "person") {
		t.Errorf("expected person in result, got: %s", result)
	}
	if !strings.Contains(result, "[clip]") {
		t.Errorf("expected [clip] marker in result, got: %s", result)
	}
}

func TestFrigateEventsEmpty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]frigateEvent{})
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	result, err := exec.Events("", "", 10)
	if err != nil {
		t.Fatalf("Events() error: %v", err)
	}
	if !strings.Contains(result, "No events") {
		t.Errorf("expected 'No events' message, got: %s", result)
	}
}

func TestFrigateEventsDefaultLimit(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("expected default limit=10, got %q", r.URL.Query().Get("limit"))
		}
		json.NewEncoder(w).Encode([]frigateEvent{})
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	exec.Events("", "", 0) // 0 should default to 10
}

func TestFrigateEventsCameraFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("camera") != "front_door" {
			t.Errorf("expected camera=front_door, got %q", r.URL.Query().Get("camera"))
		}
		json.NewEncoder(w).Encode([]frigateEvent{})
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	exec.Events("front_door", "", 10)
}

func TestFrigateEventsServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv, exec := newFrigateServer(mux)
	defer srv.Close()

	_, err := exec.Events("", "", 10)
	if err == nil {
		t.Error("expected error for 503 response")
	}
}

// --- NewFrigateExecutor ---

func TestNewFrigateExecutorStripsTrailingSlash(t *testing.T) {
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		called = true
		json.NewEncoder(w).Encode([]frigateEvent{})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	exec := NewFrigateExecutor(srv.URL + "/") // trailing slash
	exec.Events("", "", 10)
	if !called {
		t.Error("trailing slash in baseURL should not break URL construction")
	}
}
