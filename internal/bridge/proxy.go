package bridge

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/remote-relay/internal/protocol"
)

// HTTPProxy forwards plaintext RequestMessages to the local Claude Code server.
type HTTPProxy struct {
	targetURL string
	password  *string
	client    *http.Client
}

// NewHTTPProxy creates an HTTPProxy with a 30-second timeout HTTP client.
func NewHTTPProxy(targetURL string, password *string) *HTTPProxy {
	return &HTTPProxy{
		targetURL: targetURL,
		password:  password,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// HandleRequest proxies a plaintext RequestMessage to the target server and returns a ResponseMessage.
// Upstream errors are surfaced as 502/504 ResponseMessages; this method never panics.
func (p *HTTPProxy) HandleRequest(req protocol.RequestMessage) (protocol.ResponseMessage, error) {
	var bodyReader io.Reader
	if req.Body != nil && *req.Body != "" {
		bodyReader = strings.NewReader(*req.Body)
	}

	httpReq, err := http.NewRequest(req.Method, p.targetURL+req.Path, bodyReader)
	if err != nil {
		return errResponse(req.ID, 502, fmt.Sprintf("failed to construct request: %s", err.Error())), nil
	}

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	if p.password != nil {
		// Format: Authorization: Basic base64("opencode:{password}")
		creds := base64.StdEncoding.EncodeToString([]byte("opencode:" + *p.password))
		httpReq.Header.Set("Authorization", "Basic "+creds)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if isTimeoutErr(err) {
			return errResponse(req.ID, 504, "Gateway Timeout"), nil
		}
		return errResponse(req.ID, 502, fmt.Sprintf("upstream unreachable: %s", err.Error())), nil
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return errResponse(req.ID, 502, fmt.Sprintf("failed to read response body: %s", err.Error())), nil
	}

	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}

	var bodyStr *string
	if len(bodyBytes) > 0 {
		s := string(bodyBytes)
		bodyStr = &s
	}

	return protocol.ResponseMessage{
		ID:      req.ID,
		Type:    "response",
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    bodyStr,
	}, nil
}

func errResponse(id string, status int, message string) protocol.ResponseMessage {
	return protocol.ResponseMessage{
		ID:      id,
		Type:    "response",
		Status:  status,
		Headers: map[string]string{},
		Body:    &message,
	}
}

func isTimeoutErr(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}
