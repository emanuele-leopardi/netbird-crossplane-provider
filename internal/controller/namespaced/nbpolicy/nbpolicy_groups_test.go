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

package nbpolicy

import (
	"testing"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
)

// apiGroup is a group as NetBird's policy API reports it: every group carries
// both its ID and its name.
func apiGroup(id, name string) v1alpha1.GroupMinimum {
	return v1alpha1.GroupMinimum{Id: &id, Name: &name}
}

// specGroupByName is a reference written the way a user would write it, naming
// the group instead of hard-coding its generated ID.
func specGroupByName(name string) v1alpha1.GroupMinimum {
	return v1alpha1.GroupMinimum{Name: &name}
}

func specGroupByID(id string) v1alpha1.GroupMinimum {
	return v1alpha1.GroupMinimum{Id: &id}
}

func refs(g ...v1alpha1.GroupMinimum) *[]v1alpha1.GroupMinimum { return &g }

func TestGroupRefsMatch(t *testing.T) {
	cases := map[string]struct {
		reason  string
		desired *[]v1alpha1.GroupMinimum
		actual  *[]v1alpha1.GroupMinimum
		want    bool
	}{
		"NameMatches": {
			reason:  "A rule written with a group name matches the API's group of that name, so a name-based policy does not look like permanent drift.",
			desired: refs(specGroupByName("fiber-lan-cidr")),
			actual:  refs(apiGroup("id-cidr", "fiber-lan-cidr")),
			want:    true,
		},
		"IDMatches": {
			reason:  "A rule written with a group ID matches the API's group with that ID.",
			desired: refs(specGroupByID("id-cidr")),
			actual:  refs(apiGroup("id-cidr", "fiber-lan-cidr")),
			want:    true,
		},
		"OrderIgnored": {
			reason:  "NetBird may report groups in any order, which must not look like drift.",
			desired: refs(specGroupByName("fiber-routers"), specGroupByName("fiber-lan-cidr")),
			actual:  refs(apiGroup("id-cidr", "fiber-lan-cidr"), apiGroup("id-routers", "fiber-routers")),
			want:    true,
		},
		"DifferentName": {
			reason:  "A different group is drift.",
			desired: refs(specGroupByName("fiber-routers")),
			actual:  refs(apiGroup("id-cidr", "fiber-lan-cidr")),
			want:    false,
		},
		"UnknownName": {
			reason:  "A name the API does not report is drift.",
			desired: refs(specGroupByName("nope")),
			actual:  refs(apiGroup("id-cidr", "fiber-lan-cidr")),
			want:    false,
		},
		"ExtraGroupInAPI": {
			reason:  "A group the API reports but the spec does not ask for is drift.",
			desired: refs(specGroupByName("fiber-routers")),
			actual:  refs(apiGroup("id-routers", "fiber-routers"), apiGroup("id-cidr", "fiber-lan-cidr")),
			want:    false,
		},
		"MissingGroupInAPI": {
			reason:  "A group the spec asks for but the API does not report is drift.",
			desired: refs(specGroupByName("fiber-routers"), specGroupByName("fiber-lan-cidr")),
			actual:  refs(apiGroup("id-routers", "fiber-routers")),
			want:    false,
		},
		"DuplicatesDoNotDoubleCount": {
			reason:  "One API group cannot satisfy two references.",
			desired: refs(specGroupByName("fiber-routers"), specGroupByName("fiber-routers")),
			actual:  refs(apiGroup("id-routers", "fiber-routers")),
			want:    false,
		},
		"BothNil": {
			reason:  "Neither side naming groups is a match.",
			desired: nil,
			actual:  nil,
			want:    true,
		},
		"OneNil": {
			reason:  "One side naming groups and the other not is drift.",
			desired: refs(specGroupByName("fiber-routers")),
			actual:  nil,
			want:    false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := groupRefsMatch(tc.desired, tc.actual); got != tc.want {
				t.Errorf("\n%s\ngroupRefsMatch(...) = %t, want %t", tc.reason, got, tc.want)
			}
		})
	}
}
