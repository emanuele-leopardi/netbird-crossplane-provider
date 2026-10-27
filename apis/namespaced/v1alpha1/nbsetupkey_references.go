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

package v1alpha1

import (
	"context"

	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reference"
)

const (
	errResolveReferences = "cannot resolve references"
)

// ResolveReferences resolves the NbGroup references in
// spec.forProvider.auto_groups_refs and spec.forProvider.auto_groups_selector,
// populating spec.forProvider.auto_groups with the NetBird group IDs of the
// referenced groups.
//
// A NbGroup publishes its NetBird group ID as its crossplane.io/external-name
// annotation, so this is a purely local lookup: no NetBird API call is made.
// The reference is not considered resolved until the referenced group has been
// reconciled and therefore has an external name, which is what makes Crossplane
// order group creation before the setup key that depends on it.
//
// This method, rather than an explicit ReferenceResolver on the reconciler,
// implements the interface recognised by crossplane-runtime's default
// APISimpleReferenceResolver. That resolver persists the fields set here for us.
func (mg *NbSetupKey) ResolveReferences(ctx context.Context, c client.Reader) error {
	r := reference.NewAPINamespacedResolver(c, mg)

	rsp, err := r.ResolveMultiple(ctx, reference.MultiNamespacedResolutionRequest{
		CurrentValues: mg.Spec.ForProvider.AutoGroups,
		References:    mg.Spec.ForProvider.AutoGroupsRefs,
		Selector:      mg.Spec.ForProvider.AutoGroupsSelector,
		To: reference.To{
			List:    &NbGroupList{},
			Managed: &NbGroup{},
		},
		Extract:   reference.ExternalName(),
		Namespace: mg.GetNamespace(),
	})
	if err != nil {
		return errors.Wrap(err, errResolveReferences)
	}

	mg.Spec.ForProvider.AutoGroups = rsp.ResolvedValues
	mg.Spec.ForProvider.AutoGroupsRefs = rsp.ResolvedReferences

	return nil
}
