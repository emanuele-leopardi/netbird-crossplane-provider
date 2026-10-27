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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/netbirdio/netbird/management/server/http/api"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
)

func TestEqualStringSets(t *testing.T) {
	cases := map[string]struct {
		a    []string
		b    []string
		want bool
	}{
		"BothEmpty":       {a: nil, b: []string{}, want: true},
		"SameOrder":       {a: []string{"a", "b"}, b: []string{"a", "b"}, want: true},
		"DifferentOrder":  {a: []string{"b", "a"}, b: []string{"a", "b"}, want: true},
		"DifferentLength": {a: []string{"a"}, b: []string{"a", "b"}, want: false},
		"DifferentValues": {a: []string{"a"}, b: []string{"b"}, want: false},
		"OneEmpty":        {a: []string{"a"}, b: nil, want: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := equalStringSets(tc.a, tc.b); got != tc.want {
				t.Errorf("equalStringSets(%v, %v) = %t, want %t", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestIsDead(t *testing.T) {
	now := time.Now()

	cases := map[string]struct {
		reason   string
		setupkey *api.SetupKey
		want     bool
	}{
		"Valid": {
			reason:   "A valid key must not be replaced.",
			setupkey: &api.SetupKey{State: "valid"},
			want:     false,
		},
		"NoExpiry": {
			reason:   "NetBird reports the zero expiry time for a key that never expires, which must not be mistaken for an expired key.",
			setupkey: &api.SetupKey{State: "valid", Expires: time.Time{}},
			want:     false,
		},
		"Expired": {
			reason:   "An expired key must be replaced.",
			setupkey: &api.SetupKey{State: "expired", Expires: now.Add(-time.Hour)},
			want:     true,
		},
		"Revoked": {
			reason:   "A revoked key can never be un-revoked, so it must be replaced.",
			setupkey: &api.SetupKey{State: "revoked", Revoked: true},
			want:     true,
		},
		"Overused": {
			reason:   "We deliberately do not replace an overused key, so that a key with a finite usage_limit does not churn.",
			setupkey: &api.SetupKey{State: "overused"},
			want:     false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isDead(tc.setupkey); got != tc.want {
				t.Errorf("\n%s\nisDead(...) = %t, want %t", tc.reason, got, tc.want)
			}
		})
	}
}

func TestIsUpToDate(t *testing.T) {
	cases := map[string]struct {
		reason   string
		desired  []string
		observed []string
		want     bool
	}{
		"UpToDate": {
			reason:   "Identical groups are up to date.",
			desired:  []string{"a", "b"},
			observed: []string{"a", "b"},
			want:     true,
		},
		"Reordered": {
			reason:   "NetBird is free to return group IDs in any order, which must not look like drift.",
			desired:  []string{"b", "a"},
			observed: []string{"a", "b"},
			want:     true,
		},
		"BothEmpty": {
			reason:   "A key with no auto groups is up to date when NetBird reports none.",
			desired:  nil,
			observed: []string{},
			want:     true,
		},
		"AddedGroup": {
			reason:   "A group added to the spec is drift.",
			desired:  []string{"a", "b"},
			observed: []string{"a"},
			want:     false,
		},
		"RemovedGroup": {
			reason:   "A group removed from the spec is drift.",
			desired:  []string{"a"},
			observed: []string{"a", "b"},
			want:     false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := &v1alpha1.NbSetupKeyParameters{AutoGroups: tc.desired}
			k := &api.SetupKey{AutoGroups: tc.observed}
			if got := isUpToDate(p, k, ctrl.Log); got != tc.want {
				t.Errorf("\n%s\nisUpToDate(...) = %t, want %t", tc.reason, got, tc.want)
			}
		})
	}
}

// Ensure the observation round-trips the fields we now surface to users.
func TestObservationPopulated(t *testing.T) {
	k := &api.SetupKey{
		Id:         "key-id",
		Name:       "htpc",
		State:      "valid",
		Valid:      true,
		UsageLimit: 5,
		UsedTimes:  2,
		AutoGroups: []string{"group-id-htpc"},
	}
	obs := v1alpha1.NbSetupKeyObservation{
		Id:         k.Id,
		Name:       k.Name,
		State:      k.State,
		Valid:      k.Valid,
		UsageLimit: k.UsageLimit,
		UsedTimes:  k.UsedTimes,
		AutoGroups: &k.AutoGroups,
	}
	if diff := cmp.Diff([]string{"group-id-htpc"}, *obs.AutoGroups); diff != "" {
		t.Errorf("observation auto_groups: -want, +got:\n%s", diff)
	}
	if !obs.Valid || obs.State != "valid" || obs.UsedTimes != 2 || obs.UsageLimit != 5 {
		t.Errorf("observation did not carry over the key's status: %+v", obs)
	}
}
