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

package nbsetupkey

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
)

// TestObserveOnlyTreatsNotFoundAsMissing is the regression test for the bug
// that created duplicate NetBird objects: the upstream REST client discards the
// HTTP status, so Observe used to read *any* failure as "this object does not
// exist" and went on to create a second copy. Only a 404 may mean that.
func TestObserveOnlyTreatsNotFoundAsMissing(t *testing.T) {
	const key = `{
      "id": "abc123",
      "name": "fiber-peer",
      "state": "valid",
      "valid": true,
      "revoked": false,
      "ephemeral": true,
      "type": "reusable",
      "expires": "0001-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z",
      "last_used": "2026-01-01T00:00:00Z",
      "auto_groups": ["group-1"],
      "usage_limit": 0,
      "used_times": 4,
      "allow_extra_dns_labels": false,
      "key": "E2643****"
    }`

	cases := map[string]struct {
		reason     string
		status     int
		body       string
		wantExists bool
		wantErr    bool
	}{
		"NotFound": {
			reason:     "A 404 is the one answer that means the key is gone: report it missing so the reconciler issues a new one.",
			status:     http.StatusNotFound,
			body:       `{"message":"setup key not found"}`,
			wantExists: false,
		},
		"ServerError": {
			reason:     "A 500 must surface as an error, not as a missing key. Treating it as missing is what created duplicate keys and networks.",
			status:     http.StatusInternalServerError,
			body:       `{"message":"internal error"}`,
			wantExists: false,
			wantErr:    true,
		},
		"Unauthorized": {
			reason:     "A 401 is an authentication failure, which the reconciler handles by refreshing the token, never by creating a duplicate.",
			status:     http.StatusUnauthorized,
			body:       `{"message":"token invalid"}`,
			wantExists: false,
			wantErr:    true,
		},
		"Found": {
			reason:     "A key that exists is reported as existing, with its masked key left alone.",
			status:     http.StatusOK,
			body:       key,
			wantExists: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			e := external{authManager: nb.NewAuthManager(srv.URL, "test-token", "", "")}
			cr := &v1alpha1.NbSetupKey{}
			cr.SetName("fiber-peer")
			meta.SetExternalName(cr, "abc123")

			got, err := e.Observe(context.Background(), cr)
			if tc.wantErr != (err != nil) {
				t.Fatalf("\n%s\ne.Observe(...): want error %t, got %v", tc.reason, tc.wantErr, err)
			}
			if got.ResourceExists != tc.wantExists {
				t.Errorf("\n%s\ne.Observe(...).ResourceExists = %t, want %t", tc.reason, got.ResourceExists, tc.wantExists)
			}
		})
	}
}
