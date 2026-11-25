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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	nbapi "github.com/netbirdio/netbird/management/server/http/api"
)

const (
	// fiberLAN and cgnatLAN are the two network names the htpc stack uses; the
	// duplicates in the incident were two networks with one of these names.
	fiberLAN = "fiber-lan"
	cgnatLAN = "cgnat-lan"
	// netOne and netTwo are the external IDs of the two duplicates.
	netOne = "one"
	netTwo = "two"
)

func TestNetworkByName(t *testing.T) {
	cases := map[string]struct {
		reason   string
		networks []nbapi.Network
		name     string
		wantID   string
		wantErr  bool
	}{
		"Absent": {
			reason: "No match is not an error here; the caller decides whether a missing network is fatal.",
			name:   fiberLAN,
			wantID: "",
		},
		"Match": {
			reason:   "A single match resolves to that network.",
			networks: []nbapi.Network{{Id: netOne, Name: fiberLAN}},
			name:     fiberLAN,
			wantID:   netOne,
		},
		"LaterMatch": {
			reason:   "A match that is not the first entry still has to be found.",
			networks: []nbapi.Network{{Id: netOne, Name: cgnatLAN}, {Id: netTwo, Name: fiberLAN}},
			name:     fiberLAN,
			wantID:   netTwo,
		},
		"Duplicate": {
			reason: "Two networks with one name must be an error: picking one would make Observe and Create disagree and re-create objects forever.",
			networks: []nbapi.Network{
				{Id: netOne, Name: fiberLAN},
				{Id: netTwo, Name: fiberLAN},
			},
			name:    fiberLAN,
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := NetworkByName(tc.networks, tc.name)
			if tc.wantErr != (err != nil) {
				t.Fatalf("\n%s\nNetworkByName(...): want error %t, got %v", tc.reason, tc.wantErr, err)
			}
			if tc.wantErr {
				return
			}
			if tc.wantID == "" {
				if got != nil {
					t.Errorf("\n%s\nNetworkByName(...): want no match, got %v", tc.reason, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("\n%s\nNetworkByName(...): want %q, got no match", tc.reason, tc.wantID)
			}
			if diff := cmp.Diff(tc.wantID, got.Id); diff != "" {
				t.Errorf("\n%s\nNetworkByName(...).Id: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// Guard against the duplicate error losing its usefulness: it must name the
// networks so the operator can delete the right ones.
func TestNetworkByNameDuplicateMessage(t *testing.T) {
	_, err := NetworkByName([]nbapi.Network{
		{Id: netOne, Name: fiberLAN},
		{Id: netTwo, Name: fiberLAN},
	}, fiberLAN)
	if err == nil {
		t.Fatal("want an error for a duplicated network name")
	}
	for _, want := range []string{netOne, netTwo, fiberLAN} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}
