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
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/netbirdio/netbird/management/server/http/api"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
)

func TestConnectionDetails(t *testing.T) {
	cases := map[string]struct {
		reason   string
		setupkey *api.SetupKey
		want     managed.ConnectionDetails
	}{
		"Key": {
			reason:   "The key NetBird holds is published under the password key, which is the name consumers read it from.",
			setupkey: &api.SetupKey{Key: "8f4d2c0b-1234-4a5b-9c8d-abcdefabcdef"},
			want: managed.ConnectionDetails{
				"password": []byte("8f4d2c0b-1234-4a5b-9c8d-abcdefabcdef"),
			},
		},
		"NoKey": {
			reason:   "A missing setup key publishes nothing rather than a nil-valued key.",
			setupkey: nil,
			want:     managed.ConnectionDetails{},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := connectionDetails(tc.setupkey)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nconnectionDetails(...): -want, +got:\n%s", tc.reason, diff)
			}
			// The key name is a cross-repository contract: the routing peer
			// Deployment in home-clusters reads exactly this key. Assert the
			// name directly so that a rename fails here and not in the cluster.
			if tc.setupkey != nil {
				if _, ok := got["password"]; !ok {
					t.Errorf("\n%s\nconnectionDetails(...): want a %q key, got keys %v", tc.reason, "password", keys(got))
				}
			}
		})
	}
}

func keys(d managed.ConnectionDetails) []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	return out
}

func TestObserve(t *testing.T) {
	type fields struct {
		authManager *nb.AuthManager
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		// TODO: Add test cases.
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{authManager: tc.fields.authManager}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}
