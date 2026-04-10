package httputil

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// DiagnosticTransport wraps an http.RoundTripper and logs requests that take longer
// than 2 seconds, which helps in debugging unresponsive services.
type DiagnosticTransport struct {
	Transport http.RoundTripper
}

func (t *DiagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	done := make(chan struct{})

	// Start a goroutine to log if the request hangs
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				duration := time.Since(start)
				logDiagnostic(fmt.Sprintf("WARNING: Service call to %s %s has been running for %v and is still unresponsive\n", req.Method, req.URL.String(), duration))
			case <-done:
				return
			}
		}
	}()

	res, err := t.Transport.RoundTrip(req)
	close(done)
	duration := time.Since(start)

	if duration > 2*time.Second {
		logDiagnostic(fmt.Sprintf("Service call to %s %s completed after %v\n", req.Method, req.URL.String(), duration))
	}

	return res, err
}

func logDiagnostic(msg string) {
	f, err := os.OpenFile("/tmp/invariant-debug.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err == nil {
		timestamp := time.Now().Format(time.RFC3339)
		f.WriteString(fmt.Sprintf("[%s] %s", timestamp, msg))
		f.Close()
	}
}

// NewDiagnosticClient wraps the given HTTP client with a DiagnosticTransport.
// If the provided base client is nil, http.DefaultClient will be wrapped.
func NewDiagnosticClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	// Create a shallow copy of the client to avoid modifying the original
	clientCopy := *base
	clientCopy.Transport = &DiagnosticTransport{Transport: transport}
	return &clientCopy
}
