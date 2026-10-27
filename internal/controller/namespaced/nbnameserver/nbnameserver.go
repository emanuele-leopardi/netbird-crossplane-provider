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

package nbnameserver

import (
	"context"
	"reflect"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	nbapi "github.com/netbirdio/netbird/management/server/http/api"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/features"
)

const (
	errNotNbNameServer = "managed resource is not a NbNameServer custom resource"
)

// Setup adds a controller that reconciles NbNameServer managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.NbNameServerGroupKind)

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnector(&connector{
			SharedConnector: nb.NewSharedConnector(
				mgr.GetClient(),
				resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{}),
			),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(features.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}

	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.NbNameServerGroupVersionKind),
		reconcilerOptions...,
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.NbNameServer{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles NbNameServer managed resources once
// the provider has verified that their CRDs are established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.NbNameServerGroupVersionKind.String())
		}
	}, v1alpha1.NbNameServerGroupVersionKind)
	return nil
}

type connector struct {
	*nb.SharedConnector
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.NbNameServer)
	if !ok {
		return nil, errors.New(errNotNbNameServer)
	}

	authManager, err := c.SharedConnector.Connect(ctx, cr)
	if err != nil {
		return nil, err
	}

	return &external{
		authManager: authManager,
		log:         ctrl.Log.WithName("provider-nbnameserver"),
	}, nil
}

type external struct {
	authManager *nb.AuthManager
	log         logr.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.NbNameServer)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotNbNameServer)
	}

	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to get authenticated client")
	}

	// If externalName is empty, try to find the resource by Name (adoption pattern)
	externalName := meta.GetExternalName(cr)
	if externalName == "" || externalName == cr.Name {
		// Attempt to find the resource by unique name
		groups, err := client.DNS.ListNameserverGroups(ctx)
		if err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, "failed to list nameserver groups for adoption")
		}
		var found *nbapi.NameserverGroup
		for _, g := range groups {
			if g.Name == cr.Spec.ForProvider.Name {
				found = &g
				break
			}
		}
		if found != nil {
			meta.SetExternalName(cr, found.Id)
			// Return early so the reconciler persists the external name and requeues
			return managed.ExternalObservation{
				ResourceExists:   true,
				ResourceUpToDate: false, // force a requeue for a fresh Observe
			}, nil
		} else {
			return managed.ExternalObservation{ResourceExists: false}, nil
		}
	}
	// Now continue with normal observation using externalName

	nameservergroup, err := client.DNS.GetNameserverGroup(ctx, externalName)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalObservation{}, err
		}
		c.log.Info("failed to get nameserver group")
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil // return nil so that observe can return without error so that it passes to create.
	}

	cr.Status.AtProvider = v1alpha1.NbNameServerObservation{
		Id:                   nameservergroup.Id,
		Description:          nameservergroup.Description,
		Domains:              nameservergroup.Domains,
		Groups:               nameservergroup.Groups,
		Enabled:              nameservergroup.Enabled,
		Name:                 nameservergroup.Name,
		Nameservers:          *ApitoNbNameServer(nameservergroup.Nameservers),
		Primary:              nameservergroup.Primary,
		SearchDomainsEnabled: nameservergroup.SearchDomainsEnabled,
	}

	cr.Status.SetConditions(xpv2.Available())
	isUpToDate := IsNbNameServerUpToDate(*nameservergroup, *cr, c)

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: isUpToDate,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.NbNameServer)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotNbNameServer)
	}

	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to get authenticated client")
	}

	nameserverGroup, err := client.DNS.CreateNameserverGroup(ctx, nbapi.PostApiDnsNameserversJSONRequestBody{
		Description:          cr.Spec.ForProvider.Description,
		Domains:              cr.Spec.ForProvider.Domains,
		Groups:               cr.Spec.ForProvider.Groups,
		Enabled:              cr.Spec.ForProvider.Enabled,
		Name:                 cr.Spec.ForProvider.Name,
		Nameservers:          *NbtoApiNameServer(cr.Spec.ForProvider.Nameservers),
		Primary:              cr.Spec.ForProvider.Primary,
		SearchDomainsEnabled: cr.Spec.ForProvider.SearchDomainsEnabled,
	})
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to create nameserver group")
	}

	meta.SetExternalName(cr, nameserverGroup.Id)
	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.NbNameServer)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotNbNameServer)
	}

	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to get authenticated client")
	}

	_, err = client.DNS.UpdateNameserverGroup(ctx, meta.GetExternalName(cr), nbapi.PutApiDnsNameserversNsgroupIdJSONRequestBody{
		Description:          cr.Spec.ForProvider.Description,
		Domains:              cr.Spec.ForProvider.Domains,
		Groups:               cr.Spec.ForProvider.Groups,
		Enabled:              cr.Spec.ForProvider.Enabled,
		Name:                 cr.Spec.ForProvider.Name,
		Nameservers:          *NbtoApiNameServer(cr.Spec.ForProvider.Nameservers),
		Primary:              cr.Spec.ForProvider.Primary,
		SearchDomainsEnabled: cr.Spec.ForProvider.SearchDomainsEnabled,
	})
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to update nameserver group")
	}

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.NbNameServer)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotNbNameServer)
	}

	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to get authenticated client")
	}

	if err := client.DNS.DeleteNameserverGroup(ctx, meta.GetExternalName(cr)); err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to delete nameserver group")
	}

	return managed.ExternalDelete{}, nil
}

func ApitoNbNameServer(p []nbapi.Nameserver) *[]v1alpha1.Nameserver {
	nameservers := make([]v1alpha1.Nameserver, len(p))
	for i, ns := range p {
		nameservers[i] = v1alpha1.Nameserver{
			Ip:     ns.Ip,
			Port:   ns.Port,
			NsType: v1alpha1.NameserverNsType(ns.NsType),
		}
	}
	return &nameservers
}

func NbtoApiNameServer(p []v1alpha1.Nameserver) *[]nbapi.Nameserver {
	nameservers := make([]nbapi.Nameserver, len(p))
	for i, ns := range p {
		nameservers[i] = nbapi.Nameserver{
			Ip:     ns.Ip,
			Port:   ns.Port,
			NsType: nbapi.NameserverNsType(ns.NsType),
		}
	}
	return &nameservers
}

func IsNbNameServerUpToDate(p nbapi.NameserverGroup, ns v1alpha1.NbNameServer, c *external) bool {
	if !cmp.Equal(p.Description, ns.Spec.ForProvider.Description) {
		c.log.Info("description doesn't match")
		return false
	}
	if !reflect.DeepEqual(p.Domains, ns.Spec.ForProvider.Domains) {
		c.log.Info("domains don't match")
		return false
	}
	if !cmp.Equal(p.Enabled, ns.Spec.ForProvider.Enabled) {
		c.log.Info("enabled doesn't match")
		return false
	}
	if !reflect.DeepEqual(p.Groups, ns.Spec.ForProvider.Groups) {
		c.log.Info("groups don't match")
		return false
	}
	if !cmp.Equal(p.Name, ns.Spec.ForProvider.Name) {
		c.log.Info("name doesn't match")
		return false
	}
	if !cmp.Equal(len(p.Nameservers), len(ns.Spec.ForProvider.Nameservers)) {
		c.log.Info("nameservers don't match")
		return false
	}
	for i, pns := range p.Nameservers {
		if !cmp.Equal(pns.Ip, ns.Spec.ForProvider.Nameservers[i].Ip) {
			return false
		}
		if !cmp.Equal(string(pns.NsType), string(ns.Spec.ForProvider.Nameservers[i].NsType)) {
			return false
		}
		if !cmp.Equal(pns.Port, ns.Spec.ForProvider.Nameservers[i].Port) {
			return false
		}
	}
	return true
}

// Disconnect is a no-op. The provider shares a long-lived, token-refreshing
// HTTP client, so there is nothing to tear down between reconciles.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
