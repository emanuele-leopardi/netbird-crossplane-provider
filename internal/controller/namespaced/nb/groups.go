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

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
)

// ResolveGroupIDs resolves group references to NetBird group IDs.
//
// Each reference names a group or gives its ID. Every reference must resolve:
// dropping one would silently produce a policy or a network resource that does
// not grant access to the groups the author asked for.
func ResolveGroupIDs(refs []v1alpha1.GroupMinimum, groups []nbapi.Group) ([]string, error) {
	ids := make([]string, len(refs))
	for i, ref := range refs {
		id, err := resolveGroupID(ref, groups)
		if err != nil {
			return nil, errors.Wrapf(err, "group reference %d", i)
		}
		ids[i] = id
	}
	return ids, nil
}

// ResolveOptionalGroupIDs is ResolveGroupIDs for an optional list of
// references. A nil list stays nil, which keeps the field omitted from requests
// to NetBird.
func ResolveOptionalGroupIDs(refs *[]v1alpha1.GroupMinimum, groups []nbapi.Group) ([]string, error) {
	if refs == nil {
		return nil, nil
	}
	return ResolveGroupIDs(*refs, groups)
}

func resolveGroupID(ref v1alpha1.GroupMinimum, groups []nbapi.Group) (string, error) {
	switch {
	case ref.Id != nil:
		for _, g := range groups {
			if g.Id == *ref.Id {
				return g.Id, nil
			}
		}
		return "", errors.Errorf("no group with id %q", *ref.Id)

	case ref.Name != nil:
		for _, g := range groups {
			if g.Name == *ref.Name {
				return g.Id, nil
			}
		}
		return "", errors.Errorf("no group named %q", *ref.Name)

	default:
		return "", errors.New("a group reference must set either id or name")
	}
}
