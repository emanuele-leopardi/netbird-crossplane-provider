/*
Copyright 2022 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	netbird "github.com/netbirdio/netbird/management/client/rest"
	"github.com/pkg/errors"
)

// maxErrorBody bounds how much of an error response we read to build an
// APIError. The management server sends a short JSON object; anything larger is
// not something we need to quote back to the user.
const maxErrorBody = 4 << 10

// An APIError is a failed NetBird management API call that still knows its HTTP
// status code.
//
// It exists because the upstream REST client (management/client/rest) folds
// every non-2xx response into errors.New(message): the status code never
// reaches the caller. Without it, callers can only match on message text, and
// the controllers used to treat *any* error as "the object does not exist" —
// which turned a transient 500 into a duplicate resource (see the wrapped
// client below).
type APIError struct {
	// StatusCode is the HTTP status the management server answered with.
	StatusCode int
	// Message is the message the server reported, if it sent one.
	Message string
}

func (e *APIError) Error() string {
	status := http.StatusText(e.StatusCode)
	if status == "" {
		status = "unknown status"
	}
	if e.Message == "" {
		return fmt.Sprintf("netbird api: %d %s", e.StatusCode, status)
	}
	return fmt.Sprintf("netbird api: %d %s: %s", e.StatusCode, status, e.Message)
}

// IsNotFound reports whether err is the management server answering 404. Only
// that, and nothing else, means an object is gone: treating a 500 or a timeout
// as "not found" makes the caller create a second copy of the object.
func IsNotFound(err error) bool {
	return hasStatus(err, http.StatusNotFound)
}

// IsAlreadyExists reports whether err is the management server rejecting a
// create because the object is already there (409).
func IsAlreadyExists(err error) bool {
	return hasStatus(err, http.StatusConflict)
}

func hasStatus(err error, code int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == code
}

// statusErrorClient wraps the HTTP client used by the NetBird REST client so
// that a failed response comes back as an *APIError rather than as a bare
// message.
//
// The REST client's NewRequest only looks at the status code after the HTTP
// client has returned, and then throws the code away. Returning the error from
// Do instead short-circuits that: NewRequest hands our typed error straight to
// the caller, wrapped by nothing, so errors.As finds it.
type statusErrorClient struct {
	inner netbird.HttpClient
}

func (c statusErrorClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.inner.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode <= 299 {
		return resp, nil
	}
	return nil, newAPIError(resp)
}

func newAPIError(resp *http.Response) *APIError {
	apiErr := &APIError{StatusCode: resp.StatusCode}
	if resp.Body == nil {
		return apiErr
	}
	defer resp.Body.Close() //nolint:errcheck // Nothing to do about a failed close on an error path.

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return apiErr
	}
	// The server's error body is {"message": "..."} (util.ErrorResponse).
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Message != "" {
		apiErr.Message = parsed.Message
		return apiErr
	}
	// Not the usual shape (a proxy error page, an empty body): keep whatever
	// text there is, trimmed, so the message is still useful.
	apiErr.Message = strings.TrimSpace(string(body))
	return apiErr
}
