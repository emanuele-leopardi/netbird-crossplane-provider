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
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// testNamespace is the namespace the fake objects live in.
const testNamespace = "default"

func TestNbSetupKeyResolveReferences(t *testing.T) {
	type args struct {
		mg *NbSetupKey
	}

	type want struct {
		autoGroups []string
		refs       []xpv2.NamespacedReference
		err        bool
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"References": {
			reason: "A reference to a NbGroup should resolve to that group's external name, which is its NetBird group ID.",
			args: args{
				mg: &NbSetupKey{
					ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
					Spec: NbSetupKeySpec{
						ForProvider: NbSetupKeyParameters{
							AutoGroupsRefs: []xpv2.NamespacedReference{{Name: "htpc"}},
						},
					},
				},
			},
			want: want{
				autoGroups: []string{"group-id-htpc"},
				refs:       []xpv2.NamespacedReference{{Name: "htpc"}},
			},
		},
		"Selector": {
			reason: "A selector should resolve every matching NbGroup, ordered by resolved group ID.",
			args: args{
				mg: &NbSetupKey{
					ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
					Spec: NbSetupKeySpec{
						ForProvider: NbSetupKeyParameters{
							AutoGroupsSelector: &xpv2.NamespacedSelector{
								MatchLabels: map[string]string{"netbird.example.com/managed": "true"},
							},
						},
					},
				},
			},
			want: want{
				autoGroups: []string{"group-id-htpc", "group-id-servers"},
			},
		},
		"NoReferences": {
			reason: "With neither references nor a selector the desired groups are left alone.",
			args: args{
				mg: &NbSetupKey{
					ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
					Spec: NbSetupKeySpec{
						ForProvider: NbSetupKeyParameters{
							AutoGroups: []string{"explicit-id"},
						},
					},
				},
			},
			want: want{
				autoGroups: []string{"explicit-id"},
			},
		},
		"AlreadyResolved": {
			reason: "Values that are already set are cached, so an explicit auto_groups wins over references.",
			args: args{
				mg: &NbSetupKey{
					ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
					Spec: NbSetupKeySpec{
						ForProvider: NbSetupKeyParameters{
							AutoGroups:     []string{"explicit-id"},
							AutoGroupsRefs: []xpv2.NamespacedReference{{Name: "htpc"}},
						},
					},
				},
			},
			want: want{
				autoGroups: []string{"explicit-id"},
				refs:       []xpv2.NamespacedReference{{Name: "htpc"}},
			},
		},
		"UnresolvedGroup": {
			reason: "A group that has not been reconciled yet has no external name, and resolving it must fail so the setup key waits for it.",
			args: args{
				mg: &NbSetupKey{
					ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
					Spec: NbSetupKeySpec{
						ForProvider: NbSetupKeyParameters{
							AutoGroupsRefs: []xpv2.NamespacedReference{{Name: "pending"}},
						},
					},
				},
			},
			want: want{err: true},
		},
		"MissingGroup": {
			reason: "A reference to a group that does not exist must fail.",
			args: args{
				mg: &NbSetupKey{
					ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace},
					Spec: NbSetupKeySpec{
						ForProvider: NbSetupKeyParameters{
							AutoGroupsRefs: []xpv2.NamespacedReference{{Name: "absent"}},
						},
					},
				},
			},
			want: want{err: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := runtime.NewScheme()
			if err := SchemeBuilder.AddToScheme(s); err != nil {
				t.Fatalf("cannot build scheme: %v", err)
			}

			objects := []client.Object{
				group("htpc", "group-id-htpc", map[string]string{"netbird.example.com/managed": "true"}),
				group("servers", "group-id-servers", map[string]string{"netbird.example.com/managed": "true"}),
				// A group that exists but has not been reconciled yet, so it
				// has no external name.
				&NbGroup{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: testNamespace}},
			}

			c := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()

			err := tc.args.mg.ResolveReferences(context.Background(), c)
			if tc.want.err {
				if err == nil {
					t.Errorf("\n%s\nResolveReferences(...): want error, got nil", tc.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nResolveReferences(...): unexpected error: %v", tc.reason, err)
			}

			if diff := cmp.Diff(tc.want.autoGroups, tc.args.mg.Spec.ForProvider.AutoGroups); diff != "" {
				t.Errorf("\n%s\nResolveReferences(...): -want auto_groups, +got auto_groups:\n%s", tc.reason, diff)
			}
			if tc.want.refs != nil {
				if diff := cmp.Diff(tc.want.refs, tc.args.mg.Spec.ForProvider.AutoGroupsRefs); diff != "" {
					t.Errorf("\n%s\nResolveReferences(...): -want refs, +got refs:\n%s", tc.reason, diff)
				}
			}
		})
	}
}

// group returns a NbGroup that has already been reconciled, and therefore
// publishes its NetBird group ID as its external name.
func group(name, id string, labels map[string]string) *NbGroup {
	g := &NbGroup{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels}}
	meta.SetExternalName(g, id)
	return g
}
