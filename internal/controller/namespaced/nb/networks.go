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
	"github.com/pkg/errors"

	nbapi "github.com/netbirdio/netbird/management/server/http/api"
)

// NetworkByName returns the network that has the given name, or nil when no
// network matches.
//
// More than one match is an error, not a choice. NetBird does not enforce
// unique network names, and a controller that silently picks one match while
// another code path picks a different one can never converge: Observe would
// look at one network while Create wrote to the other. That is exactly how
// duplicate networks appeared in the first place.
func NetworkByName(networks []nbapi.Network, name string) (*nbapi.Network, error) {
	var found *nbapi.Network
	for i := range networks {
		if networks[i].Name != name {
			continue
		}
		if found != nil {
			return nil, errors.Errorf("multiple networks named %q (%s and %s): delete the duplicates", name, found.Id, networks[i].Id)
		}
		found = &networks[i]
	}
	return found, nil
}
