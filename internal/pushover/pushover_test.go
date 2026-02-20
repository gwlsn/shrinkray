package pushover

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestIsConfigured(t *testing.T) {
	// Table-driven tests covering all combinations of empty/set credentials.
	// IsConfigured requires BOTH UserKey and AppToken to be non-empty.
	tests := []struct {
		name     string
		userKey  string
		appToken string
		want     bool
	}{
		{"both empty", "", "", false},
		{"only user key", "user123", "", false},
		{"only app token", "", "token456", false},
		{"both set", "user123", "token456", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(tt.userKey, tt.appToken)
			if got := c.IsConfigured(); got != tt.want {
				t.Errorf("IsConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSendUnconfigured(t *testing.T) {
	// Send should return an error immediately if credentials are missing,
	// without making any HTTP call. This guards against accidental API calls
	// when the client was constructed with empty credentials.
	c := NewClient("", "")
	err := c.Send("Title", "Message")
	if err == nil {
		t.Fatal("expected error when credentials not configured")
	}
	if err.Error() != "pushover credentials not configured" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestSendSuccess(t *testing.T) {
	// We spin up an httptest.Server that mimics the Pushover API.
	// To redirect the client's request (which targets the hardcoded apiURL
	// constant) to our test server, we temporarily swap http.DefaultTransport
	// with a custom RoundTripper. This is a standard Go test pattern for code
	// that uses http.DefaultClient or top-level http functions like PostForm.
	// Use a channel to hand off the parsed body from the server goroutine
	// to the test goroutine. This avoids a data race: the httptest server
	// handler runs in its own goroutine, so writing to a shared variable
	// without synchronization would trip the race detector.
	bodyCh := make(chan url.Values, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read and parse the form body so we can verify its contents
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("failed to read request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		parsed, err := url.ParseQuery(string(body))
		if err != nil {
			t.Errorf("failed to parse form body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		bodyCh <- parsed

		// Return 200 OK (success response)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]int{"status": 1})
	}))
	defer server.Close()

	// Swap the default transport so http.PostForm(apiURL, ...) gets routed
	// to our test server instead of the real Pushover API.
	origTransport := http.DefaultTransport
	http.DefaultTransport = &redirectTransport{testServerURL: server.URL, original: origTransport}
	defer func() { http.DefaultTransport = origTransport }()

	c := NewClient("myuser", "mytoken")
	err := c.Send("Test Title", "Test Message")
	if err != nil {
		t.Fatalf("Send() returned unexpected error: %v", err)
	}

	// Receive the parsed body from the handler goroutine via the channel.
	receivedBody := <-bodyCh

	// Verify the request body contained the correct fields.
	// This confirms Send() is building the POST form data correctly.
	if receivedBody.Get("token") != "mytoken" {
		t.Errorf("expected token 'mytoken', got %q", receivedBody.Get("token"))
	}
	if receivedBody.Get("user") != "myuser" {
		t.Errorf("expected user 'myuser', got %q", receivedBody.Get("user"))
	}
	if receivedBody.Get("title") != "Test Title" {
		t.Errorf("expected title 'Test Title', got %q", receivedBody.Get("title"))
	}
	if receivedBody.Get("message") != "Test Message" {
		t.Errorf("expected message 'Test Message', got %q", receivedBody.Get("message"))
	}
}

func TestSendAPIError(t *testing.T) {
	// When Pushover returns a non-200 status with error details in JSON,
	// Send() should parse the errors array and return a descriptive message.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"errors": []string{"invalid token", "user not found"},
			"status": 0,
		})
	}))
	defer server.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = &redirectTransport{testServerURL: server.URL, original: origTransport}
	defer func() { http.DefaultTransport = origTransport }()

	c := NewClient("baduser", "badtoken")
	err := c.Send("Title", "Message")
	if err == nil {
		t.Fatal("expected error for API error response")
	}

	// The error message should contain the joined errors from the API response
	expected := "pushover error: invalid token, user not found"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestSendNonOKWithoutErrorBody(t *testing.T) {
	// When the API returns a non-200 status but the body is not parseable
	// JSON with an errors array, Send() falls back to reporting the status code.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	}))
	defer server.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = &redirectTransport{testServerURL: server.URL, original: origTransport}
	defer func() { http.DefaultTransport = origTransport }()

	c := NewClient("user", "token")
	err := c.Send("Title", "Message")
	if err == nil {
		t.Fatal("expected error for 500 response")
	}

	expected := "pushover returned status 500"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestTest(t *testing.T) {
	// Test() is a convenience wrapper that calls Send with a known title
	// and message. We verify it sends "Shrinkray" as the title and the
	// expected test notification body.
	var receivedBody url.Values

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody, _ = url.ParseQuery(string(body))
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]int{"status": 1})
	}))
	defer server.Close()

	origTransport := http.DefaultTransport
	http.DefaultTransport = &redirectTransport{testServerURL: server.URL, original: origTransport}
	defer func() { http.DefaultTransport = origTransport }()

	c := NewClient("testuser", "testtoken")
	err := c.Test()
	if err != nil {
		t.Fatalf("Test() returned unexpected error: %v", err)
	}

	if receivedBody.Get("title") != "Shrinkray" {
		t.Errorf("expected title 'Shrinkray', got %q", receivedBody.Get("title"))
	}
	if receivedBody.Get("message") != "Test notification - Pushover is configured correctly!" {
		t.Errorf("unexpected message: %q", receivedBody.Get("message"))
	}
}

func TestTestUnconfigured(t *testing.T) {
	// Test() should propagate the "not configured" error from Send()
	// when credentials are missing.
	c := NewClient("", "")
	err := c.Test()
	if err == nil {
		t.Fatal("expected error when credentials not configured")
	}
}

// redirectTransport is a custom http.RoundTripper that rewrites the request URL
// to point at our httptest.Server. This lets us intercept calls to the hardcoded
// apiURL constant without modifying production code. Only the scheme and host
// are changed; the path is preserved so the request structure stays intact.
//
// We store a reference to the original transport because once we replace
// http.DefaultTransport with this struct, we can no longer type-assert
// http.DefaultTransport back to *http.Transport. Without this, the
// RoundTrip call would panic.
type redirectTransport struct {
	testServerURL string
	original      http.RoundTripper
}

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	testURL, err := url.Parse(rt.testServerURL)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = testURL.Scheme
	req.URL.Host = testURL.Host
	return rt.original.RoundTrip(req)
}
