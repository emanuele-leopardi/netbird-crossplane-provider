/*
Copyright 2025 The Crossplane Authors.

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

// Package clients contains helpers shared by the provider's managed resource
// controllers.
package clients

import (
	"context"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
)

const (
	errProviderConfigNotSet = "provider config is not set"
	errGetProviderConfig    = "cannot get provider config"
	errFailedToTrackUsage   = "cannot track provider config usage"
	errUnknownProviderKind  = "unknown provider config kind"
)

// ResolveProviderConfig returns the ProviderConfig a namespaced managed
// resource refers to, after tracking that the resource is using it.
//
// A managed resource may refer to either a namespaced ProviderConfig (which
// must live in the same namespace as the managed resource) or a cluster-scoped
// ClusterProviderConfig. The returned value is always rendered as a
// ClusterProviderConfig so callers can treat both cases identically.
func ResolveProviderConfig(ctx context.Context, kube client.Client, usage resource.ModernTracker, mg resource.ModernManaged) (*v1alpha1.ClusterProviderConfig, error) {
	ref := mg.GetProviderConfigReference()
	if ref == nil || ref.Name == "" {
		return nil, errors.New(errProviderConfigNotSet)
	}

	kind := ref.Kind
	if kind == "" {
		kind = v1alpha1.ClusterProviderConfigKind
	}

	obj, err := kube.Scheme().New(v1alpha1.SchemeGroupVersion.WithKind(kind))
	if err != nil {
		return nil, errors.Wrapf(err, "referenced provider config kind %q is invalid for %s/%s", kind, mg.GetNamespace(), mg.GetName())
	}
	pc, ok := obj.(resource.ProviderConfig)
	if !ok {
		return nil, errors.Errorf("referenced provider config kind %q is not a provider config type", kind)
	}

	// The namespace is ignored when the referenced object is cluster-scoped.
	if err := kube.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: mg.GetNamespace()}, pc); err != nil {
		return nil, errors.Wrap(err, errGetProviderConfig)
	}

	if usage != nil {
		if err := usage.Track(ctx, mg); err != nil {
			return nil, errors.Wrap(err, errFailedToTrackUsage)
		}
	}

	switch p := pc.(type) {
	case *v1alpha1.ProviderConfig:
		out := &v1alpha1.ClusterProviderConfig{
			TypeMeta: metav1.TypeMeta{
				APIVersion: v1alpha1.SchemeGroupVersion.String(),
				Kind:       v1alpha1.ClusterProviderConfigKind,
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:        p.GetName(),
				UID:         p.GetUID(),
				Labels:      p.GetLabels(),
				Annotations: p.GetAnnotations(),
			},
			Spec: p.Spec,
		}
		// Secret references on a namespaced ProviderConfig are local to the
		// namespace the config lives in, which is the namespace the managed
		// resource lives in too.
		if out.Spec.Credentials.SecretRef != nil {
			out.Spec.Credentials.SecretRef.Namespace = mg.GetNamespace()
		}
		return out, nil
	case *v1alpha1.ClusterProviderConfig:
		return p, nil
	default:
		return nil, errors.New(errUnknownProviderKind)
	}
}
