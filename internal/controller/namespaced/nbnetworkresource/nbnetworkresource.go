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

package nbnetworkresource

import (
	"context"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/go-logr/logr"
	nbapi "github.com/netbirdio/netbird/management/server/http/api"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/apis/namespaced/v1alpha1"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/controller/namespaced/nb"
	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/features"
)

const (
	errNotNbNetworkResource = "managed resource is not a NbNetworkResource custom resource"
	errTrackPCUsage         = "cannot track ProviderConfig usage"
	errGetPC                = "cannot get ProviderConfig"
	errGetCreds             = "cannot get credentials"
)

// Setup adds a controller that reconciles NbNetworkResource managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.NbNetworkResourceGroupKind)

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
		resource.ManagedKind(v1alpha1.NbNetworkResourceGroupVersionKind),
		reconcilerOptions...,
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.NbNetworkResource{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// SetupGated adds a controller that reconciles NbNetworkResource managed resources once
// the provider has verified that their CRDs are established.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup reconciler", "gvk", v1alpha1.NbNetworkResourceGroupVersionKind.String())
		}
	}, v1alpha1.NbNetworkResourceGroupVersionKind)
	return nil
}

type connector struct {
	*nb.SharedConnector
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.NbNetworkResource)
	if !ok {
		return nil, errors.New(errNotNbNetworkResource)
	}

	authManager, err := c.SharedConnector.Connect(ctx, cr)
	if err != nil {
		return nil, err
	}

	return &external{
		authManager: authManager,
		log:         ctrl.Log.WithName("provider-nbnetworkresource"),
	}, nil
}

type external struct {
	authManager *nb.AuthManager
	log         logr.Logger
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.NbNetworkResource)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotNbNetworkResource)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("observing", "cr", cr)
	externalName := meta.GetExternalName(cr)

	// Adoption pattern: if externalName is blank or matches resource name, try to find by Name
	if externalName == "" || externalName == cr.Name {
		networks, err := client.Networks.List(ctx)
		if err != nil {
			if nb.IsTokenInvalidError(err) {
				if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
					c.log.Error(refreshErr, "failed to force token refresh")
				}
				return managed.ExternalObservation{}, err
			}
			c.log.Info("failed to list networks")
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil // return nil so that observe can return without error so that it passes to create.
		}
		var apinetwork *nbapi.Network
		for _, network := range networks {
			if network.Name == cr.Spec.ForProvider.NetworkName {
				apinetwork = &network
				break
			}
		}
		if apinetwork == nil {
			return managed.ExternalObservation{ResourceExists: false}, errors.New("network name not found")
		}
		resources, err := client.Networks.Resources(apinetwork.Id).List(ctx)
		if err != nil {
			if nb.IsTokenInvalidError(err) {
				if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
					c.log.Error(refreshErr, "failed to force token refresh")
				}
				return managed.ExternalObservation{}, err
			}
			c.log.Info("failed to list network resources")
			return managed.ExternalObservation{
				ResourceExists: false,
			}, nil // return nil so that observe can return without error so that it passes to create.
		}
		for _, res := range resources {
			if res.Name == cr.Spec.ForProvider.Name {
				meta.SetExternalName(cr, res.Id)
				cr.Status.AtProvider = v1alpha1.NbNetworkResourceObservation{
					Id:          res.Id,
					Enabled:     res.Enabled,
					Address:     res.Address,
					Description: res.Description,
					Groups:      convertGroups(res.Groups),
					Name:        res.Name,
					Type:        string(res.Type),
				}
				cr.Status.SetConditions(xpv2.Available())
				return managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false, // force requeue to persist external name
				}, nil
			}
		}
		// Not found by name, treat as not existing
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// If we have an external name (and it's not just the resource name), fetch by ID
	networks, err := client.Networks.List(ctx)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalObservation{}, err
		}
		c.log.Info("failed to list networks")
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil // return nil so that observe can return without error so that it passes to create.
	}
	var apinetwork *nbapi.Network
	for _, network := range networks {
		if network.Name == cr.Spec.ForProvider.NetworkName {
			apinetwork = &network
			break
		}
	}
	if apinetwork == nil {
		return managed.ExternalObservation{ResourceExists: false}, errors.New("network name not found")
	}
	networkresource, err := client.Networks.Resources(apinetwork.Id).Get(ctx, externalName)
	if err != nil {
		if nb.IsTokenInvalidError(err) {
			if refreshErr := c.authManager.ForceRefresh(ctx); refreshErr != nil {
				c.log.Error(refreshErr, "failed to force token refresh")
			}
			return managed.ExternalObservation{}, err
		}
		c.log.Info("failed to get network resource")
		return managed.ExternalObservation{
			ResourceExists: false,
		}, nil // return nil so that observe can return without error so that it passes to create.
	}
	cr.Status.AtProvider = v1alpha1.NbNetworkResourceObservation{
		Id:          networkresource.Id,
		Enabled:     networkresource.Enabled,
		Address:     networkresource.Address,
		Description: networkresource.Description,
		Groups:      convertGroups(networkresource.Groups),
		Name:        networkresource.Name,
		Type:        string(networkresource.Type),
	}

	cr.Status.SetConditions(xpv2.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true, // TODO: implement up-to-date check if needed
	}, nil
}

func convertGroups(groupMinimums []nbapi.GroupMinimum) *[]v1alpha1.GroupMinimum {
	groups := make([]v1alpha1.GroupMinimum, len(groupMinimums))
	for i, g := range groupMinimums {
		groups[i] = v1alpha1.GroupMinimum{
			Id: &g.Id,
			// Issued:         &g.Issued,
			Name:           &g.Name,
			PeersCount:     g.PeersCount,
			ResourcesCount: g.ResourcesCount,
		}
	}
	return &groups
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.NbNetworkResource)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotNbNetworkResource)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("creating", "cr", cr)
	networks, err := client.Networks.List(ctx)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	var apinetwork *nbapi.Network
	for _, network := range networks {
		if network.Name == cr.Spec.ForProvider.NetworkName {
			apinetwork = &network
		}
	}
	if apinetwork == nil {
		return managed.ExternalCreation{}, errors.New("network name not found")
	}
	groups, err := client.Groups.List(ctx)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	groupids, err := nb.ResolveOptionalGroupIDs(cr.Spec.ForProvider.Groups, groups)
	if err != nil {
		return managed.ExternalCreation{}, err
	}
	networkresource, err := client.Networks.Resources(apinetwork.Id).Create(ctx, nbapi.NetworkResourceRequest{
		Enabled:     cr.Spec.ForProvider.Enabled,
		Address:     cr.Spec.ForProvider.Address,
		Description: cr.Spec.ForProvider.Description,
		Groups:      groupids,
		Name:        cr.Spec.ForProvider.Name,
	})

	if err != nil {
		return managed.ExternalCreation{}, err
	}
	meta.SetExternalName(cr, networkresource.Id)
	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.NbNetworkResource)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotNbNetworkResource)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, "failed to get authenticated client")
	}
	networkResourceId := meta.GetExternalName(cr)
	c.log.Info("Updating", "cr", cr)
	networks, err := client.Networks.List(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	var apinetwork *nbapi.Network
	for _, network := range networks {
		if network.Name == cr.Spec.ForProvider.NetworkName {
			apinetwork = &network
		}
	}
	if apinetwork == nil {
		return managed.ExternalUpdate{}, errors.New("network name not found")
	}
	groups, err := client.Groups.List(ctx)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	groupids, err := nb.ResolveOptionalGroupIDs(cr.Spec.ForProvider.Groups, groups)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	_, err2 := client.Networks.Resources(apinetwork.Id).Update(ctx, networkResourceId, nbapi.PutApiNetworksNetworkIdResourcesResourceIdJSONRequestBody{
		Enabled:     cr.Spec.ForProvider.Enabled,
		Address:     cr.Spec.ForProvider.Address,
		Description: cr.Spec.ForProvider.Description,
		Groups:      groupids,
		Name:        cr.Spec.ForProvider.Name,
	})
	if err2 != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*v1alpha1.NbNetworkResource)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotNbNetworkResource)
	}
	client, err := c.authManager.GetClient(ctx)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, "failed to get authenticated client")
	}
	c.log.Info("Deleting", "cr", cr)
	networks, err := client.Networks.List(ctx)
	if err != nil {
		return managed.ExternalDelete{}, err
	}
	var apinetwork *nbapi.Network
	for _, network := range networks {
		if network.Name == cr.Spec.ForProvider.NetworkName {
			apinetwork = &network
		}
	}
	if apinetwork == nil {
		return managed.ExternalDelete{}, errors.New("network not found")
	}
	networkresourceid := meta.GetExternalName(cr)
	return managed.ExternalDelete{}, client.Networks.Resources(apinetwork.Id).Delete(ctx, networkresourceid)
}

// Disconnect is a no-op. The provider shares a long-lived, token-refreshing
// HTTP client, so there is nothing to tear down between reconciles.
func (c *external) Disconnect(_ context.Context) error {
	return nil
}
