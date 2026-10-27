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
	"testing"

	"github.com/google/go-cmp/cmp"

	nbapi "github.com/netbirdio/netbird/management/server/http/api"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
)

// The two groups the tests resolve against. NetBird generates a group's ID, so
// a reference written by a user names the group instead.
const (
	routersName = "fiber-routers"
	routersID   = "id-routers"
	cidrName    = "fiber-lan-cidr"
	cidrID      = "id-cidr"
	noSuchGroup = "nope"
)

func refByName(n string) v1alpha1.GroupMinimum { return v1alpha1.GroupMinimum{Name: &n} }
func refByID(id string) v1alpha1.GroupMinimum  { return v1alpha1.GroupMinimum{Id: &id} }

func TestResolveGroupIDs(t *testing.T) {
	// The groups NetBird reports, each with the ID and the name it is known by.
	groups := []nbapi.Group{
		{Id: routersID, Name: routersName},
		{Id: cidrID, Name: cidrName},
	}

	cases := map[string]struct {
		reason string
		refs   []v1alpha1.GroupMinimum
		want   []string
		wantOK bool
	}{
		"ByName": {
			reason: "A group named in the spec resolves to its NetBird ID.",
			refs:   []v1alpha1.GroupMinimum{refByName(cidrName)},
			want:   []string{cidrID},
			wantOK: true,
		},
		"ByID": {
			reason: "A group given by ID resolves to itself.",
			refs:   []v1alpha1.GroupMinimum{refByID(routersID)},
			want:   []string{routersID},
			wantOK: true,
		},
		"Mixed": {
			reason: "References may mix IDs and names and keep their order.",
			refs:   []v1alpha1.GroupMinimum{refByName(routersName), refByID(cidrID)},
			want:   []string{routersID, cidrID},
			wantOK: true,
		},
		"Empty": {
			reason: "No references resolve to no IDs.",
			refs:   []v1alpha1.GroupMinimum{},
			want:   []string{},
			wantOK: true,
		},
		"UnknownName": {
			reason: "A name that no group has must fail rather than resolve to an empty ID.",
			refs:   []v1alpha1.GroupMinimum{refByName(noSuchGroup)},
			wantOK: false,
		},
		"UnknownID": {
			reason: "An ID that no group has must fail.",
			refs:   []v1alpha1.GroupMinimum{refByID(noSuchGroup)},
			wantOK: false,
		},
		"Neither": {
			reason: "A reference that sets neither id nor name must fail.",
			refs:   []v1alpha1.GroupMinimum{{}},
			wantOK: false,
		},
		"OneOfTwoUnresolved": {
			reason: "Every reference must resolve: the first resolving must not mask the second failing, because the caller would otherwise be handed an empty group ID and silently grant access to nothing.",
			refs:   []v1alpha1.GroupMinimum{refByName(routersName), refByName(noSuchGroup)},
			wantOK: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveGroupIDs(tc.refs, groups)

			if !tc.wantOK {
				if err == nil {
					t.Errorf("\n%s\nResolveGroupIDs(...): want error, got %v", tc.reason, got)
				}
				if got != nil {
					t.Errorf("\n%s\nResolveGroupIDs(...): want nil IDs on error, got %v", tc.reason, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("\n%s\nResolveGroupIDs(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nResolveGroupIDs(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestResolveOptionalGroupIDs(t *testing.T) {
	groups := []nbapi.Group{{Id: routersID, Name: routersName}}

	cases := map[string]struct {
		reason string
		refs   *[]v1alpha1.GroupMinimum
		want   []string
		wantOK bool
	}{
		"Nil": {
			reason: "An omitted field stays nil so that it is left out of the request body.",
			refs:   nil,
			want:   nil,
			wantOK: true,
		},
		"Resolved": {
			reason: "A non-nil field is resolved as usual.",
			refs:   &[]v1alpha1.GroupMinimum{refByName(routersName)},
			want:   []string{routersID},
			wantOK: true,
		},
		"Unresolved": {
			reason: "A non-nil field that does not resolve must fail rather than panic.",
			refs:   &[]v1alpha1.GroupMinimum{refByName(noSuchGroup)},
			wantOK: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveOptionalGroupIDs(tc.refs, groups)

			if !tc.wantOK {
				if err == nil {
					t.Errorf("\n%s\nResolveOptionalGroupIDs(...): want error, got %v", tc.reason, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("\n%s\nResolveOptionalGroupIDs(...): unexpected error: %v", tc.reason, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nResolveOptionalGroupIDs(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
