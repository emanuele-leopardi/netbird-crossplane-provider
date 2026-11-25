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
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	nbclient "github.com/netbirdio/netbird/management/client/rest"
	pkgerrors "github.com/pkg/errors"
)

// a stubHTTPClient stands in for the real HTTP client so the wrapper can be
// exercised without a server.
type stubHTTPClient struct {
	resp *http.Response
	err  error
}

func (s stubHTTPClient) Do(*http.Request) (*http.Response, error) { return s.resp, s.err }

func TestStatusErrorClient(t *testing.T) {
	cases := map[string]struct {
		reason     string
		status     int
		body       string
		wantErr    bool
		wantStatus int
		wantMsg    string
	}{
		"Success": {
			reason: "A 2xx response must pass through untouched: NewRequest still has to parse the body.",
			status: http.StatusOK,
		},
		"NotFound": {
			reason:     "A 404 becomes a typed error carrying 404, which is the only signal a caller can use to mean 'this object is gone'.",
			status:     http.StatusNotFound,
			body:       `{"message":"network not found"}`,
			wantErr:    true,
			wantStatus: http.StatusNotFound,
			wantMsg:    "network not found",
		},
		"ServerError": {
			reason:     "A 500 also becomes a typed error; it is emphatically not a 404, and that distinction is the fix.",
			status:     http.StatusInternalServerError,
			body:       `{"message":"internal error"}`,
			wantErr:    true,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "internal error",
		},
		"NotJSON": {
			reason:     "A proxy or gateway that answers with plain text must still yield the status code.",
			status:     http.StatusBadGateway,
			body:       "upstream timeout\n",
			wantErr:    true,
			wantStatus: http.StatusBadGateway,
			wantMsg:    "upstream timeout",
		},
		"EmptyBody": {
			reason:     "An empty error body must not cost us the status code.",
			status:     http.StatusForbidden,
			body:       "",
			wantErr:    true,
			wantStatus: http.StatusForbidden,
			wantMsg:    "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := statusErrorClient{inner: stubHTTPClient{resp: &http.Response{
				StatusCode: tc.status,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}}}
			resp, err := c.Do(&http.Request{})
			if tc.wantErr != (err != nil) {
				t.Fatalf("\n%s\nstatusErrorClient.Do(...): want error %t, got %v", tc.reason, tc.wantErr, err)
			}
			if !tc.wantErr {
				if resp == nil {
					t.Fatalf("\n%s\nstatusErrorClient.Do(...): want the response back, got nil", tc.reason)
				}
				return
			}
			if resp != nil {
				t.Errorf("\n%s\nstatusErrorClient.Do(...): want nil response on error, got one", tc.reason)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("\n%s\nstatusErrorClient.Do(...): want *APIError, got %T: %v", tc.reason, err, err)
			}
			if diff := cmp.Diff(tc.wantStatus, apiErr.StatusCode); diff != "" {
				t.Errorf("\n%s\nAPIError.StatusCode: -want, +got:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.wantMsg, apiErr.Message); diff != "" {
				t.Errorf("\n%s\nAPIError.Message: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	cases := map[string]struct {
		reason string
		err    error
		want   bool
	}{
		"Nil": {
			reason: "No error is not a 404, or callers would create on every clean reconcile.",
			err:    nil,
			want:   false,
		},
		"NotFound": {
			reason: "The 404 itself is the one answer that means not found.",
			err:    &APIError{StatusCode: http.StatusNotFound},
			want:   true,
		},
		"Wrapped": {
			reason: "Controllers wrap API errors as they pass them up, so the check has to see through that.",
			err:    pkgerrors.Wrap(&APIError{StatusCode: http.StatusNotFound}, "failed to get network"),
			want:   true,
		},
		"ServerError": {
			reason: "A 500 must never be mistaken for a missing object: that is what created duplicate networks.",
			err:    &APIError{StatusCode: http.StatusInternalServerError},
			want:   false,
		},
		"Conflict": {
			reason: "A 409 is about an object that does exist.",
			err:    &APIError{StatusCode: http.StatusConflict},
			want:   false,
		},
		"BareMessage": {
			reason: "The old client produced plain messages with no status; those must not be read as not-found.",
			err:    errors.New("network not found"),
			want:   false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("\n%s\nIsNotFound(%v) = %t, want %t", tc.reason, tc.err, got, tc.want)
			}
		})
	}
}

func TestAPIErrorError(t *testing.T) {
	cases := map[string]struct {
		reason string
		err    *APIError
		want   string
	}{
		"WithMessage": {
			reason: "The message and the status both have to reach the log, since the status is what callers branch on.",
			err:    &APIError{StatusCode: http.StatusNotFound, Message: "network not found"},
			want:   "netbird api: 404 Not Found: network not found",
		},
		"WithoutMessage": {
			reason: "A body-less error still identifies itself by status.",
			err:    &APIError{StatusCode: http.StatusInternalServerError},
			want:   "netbird api: 500 Internal Server Error",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.err.Error()); diff != "" {
				t.Errorf("\n%s\nAPIError.Error(): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// Compile-time check that the wrapper satisfies the interface the NetBird
// client accepts, which is what makes WithHttpClient usable here.
var _ nbclient.HttpClient = statusErrorClient{}
