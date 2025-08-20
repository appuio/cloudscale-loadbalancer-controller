/*
Copyright 2025.

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

package v1beta1

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	cloudscalev1beta1 "github.com/appuio/cloudscale-loadbalancer-controller/api/v1beta1"
)

// SetupLoadBalancerWebhookWithManager registers the webhook for LoadBalancer in the manager.
func SetupLoadBalancerWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&cloudscalev1beta1.LoadBalancer{}).
		WithValidator(&LoadBalancerCustomValidator{
			Client: mgr.GetClient(),
		}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-cloudscale-appuio-io-v1beta1-loadbalancer,mutating=false,failurePolicy=fail,sideEffects=None,groups=cloudscale.appuio.io,resources=loadbalancers,verbs=create;update,versions=v1beta1,name=validate-cloudscale-v1beta1-loadbalancer.appuio.io,admissionReviewVersions=v1

// LoadBalancerCustomValidator struct is responsible for validating the LoadBalancer resource
// when it is created, updated, or deleted.
type LoadBalancerCustomValidator struct {
	Client client.Client
}

var _ webhook.CustomValidator = &LoadBalancerCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type LoadBalancer.
func (v *LoadBalancerCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	newLB, ok := obj.(*cloudscalev1beta1.LoadBalancer)
	if !ok {
		return nil, fmt.Errorf("expected a LoadBalancer object but got %T", obj)
	}

	return nil, validationErrorsToError(v.verify(ctx, nil, newLB))
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type LoadBalancer.
func (v *LoadBalancerCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newLB, ok := newObj.(*cloudscalev1beta1.LoadBalancer)
	if !ok {
		return nil, fmt.Errorf("expected a LoadBalancer object for the newObj but got %T", newObj)
	}
	oldLB, ok := oldObj.(*cloudscalev1beta1.LoadBalancer)
	if !ok {
		return nil, fmt.Errorf("expected a LoadBalancer object for the oldObj but got %T", oldObj)
	}

	return nil, validationErrorsToError(v.verify(ctx, oldLB, newLB))
}

func (v *LoadBalancerCustomValidator) verify(ctx context.Context, oldLB *cloudscalev1beta1.LoadBalancer, newLB *cloudscalev1beta1.LoadBalancer) []string {
	var validationErrors []string

	for i, vip := range newLB.Spec.VirtualIPAddresses {
		if vip.Address == "" {
			continue
		}
		if net.ParseIP(vip.Address) == nil {
			validationErrors = append(validationErrors, fmt.Sprintf(".spec.virtualIPAddresses[%d].address: Invalid virtual IP address: %s", i, vip.Address))
		}
	}

	usedIPs, err := v.usedFloatingIPs(ctx)
	if err != nil {
		validationErrors = append(validationErrors, fmt.Sprintf(".spec.floatingIPAddresses: Failed to retrieve floating IPs already in use: %v", err))
	} else {
		for i, fip := range newLB.Spec.FloatingIPAddresses {
			_, _, err := net.ParseCIDR(fip.CIDR)
			if err != nil {
				validationErrors = append(validationErrors, fmt.Sprintf(".spec.floatingIPAddresses[%d].cidr: Invalid floating IP address: %s", i, fip.CIDR))
			}
			usedBy, used := usedIPs[fip.CIDR]
			if used && (usedBy.Name != newLB.Name || usedBy.Namespace != newLB.Namespace) {
				validationErrors = append(validationErrors, fmt.Sprintf(".spec.floatingIPAddresses[%d].cidr: Floating IP %s is already assigned to another LoadBalancer: %s/%s", i, fip.CIDR, usedBy.Namespace, usedBy.Name))
			}
		}
	}

	for i, pool := range newLB.Spec.Pools {
		for j, code := range pool.Backend.HealthMonitor.HTTP.StatusCodes {
			valid, isRange := checkStatusCodeRange(code)
			if !valid {
				validationErrors = append(validationErrors, fmt.Sprintf(".spec.pools[%d].backend.healthCheck.http.statusCodes[%d]: Invalid status code: %s", i, j, code))
			}
			if isRange && len(pool.Backend.HealthMonitor.HTTP.StatusCodes) > 1 {
				validationErrors = append(validationErrors, fmt.Sprintf(".spec.pools[%d].backend.healthCheck.http.statusCodes[%d]: Status code ranges are not allowed when multiple status codes are specified", i, j))
				break
			}
		}

		for j, ac := range pool.Frontend.AllowedCIDRs {
			if _, _, err := net.ParseCIDR(ac.CIDR); err != nil {
				validationErrors = append(validationErrors, fmt.Sprintf(".spec.pools[%d].frontend.allowedCIDRs[%d]: Invalid allowed CIDR: %s", i, j, ac.CIDR))
			}
		}
	}

	if oldLB != nil {
		validationErrors = append(validationErrors, v.verifyChanges(oldLB, newLB)...)
	}

	return validationErrors
}

func (v *LoadBalancerCustomValidator) verifyChanges(oldLB, newLB *cloudscalev1beta1.LoadBalancer) []string {
	var validationErrors []string

	if oldLB.Spec.UUID != "" && newLB.Spec.UUID != oldLB.Spec.UUID {
		validationErrors = append(validationErrors, ".spec.uuid: UUID cannot be changed once set")
	}
	if oldLB.Spec.Zone != "" && newLB.Spec.Zone != oldLB.Spec.Zone {
		validationErrors = append(validationErrors, ".spec.zone: Zone cannot be changed once set")
	}

	vipsf := func(a, b cloudscalev1beta1.VirtualIPAddress) int {
		return 10*strings.Compare(a.SubnetID, b.SubnetID) + strings.Compare(a.Address, b.Address)
	}
	oldVIPs := slices.Clone(oldLB.Spec.VirtualIPAddresses)
	slices.SortFunc(oldVIPs, vipsf)
	newVIPs := slices.Clone(newLB.Spec.VirtualIPAddresses)
	slices.SortFunc(newVIPs, vipsf)

	if !slices.Equal(oldVIPs, newVIPs) {
		validationErrors = append(validationErrors, ".spec.virtualIPAddresses: Virtual IP addresses cannot be changed once set")
	}

	return validationErrors
}

var statusCodeRegex = regexp.MustCompile(`^([1-5]\d\d)(?:-[1-5]\d\d)?$`)

// checkStatusCodeRange checks if the provided status code is a valid HTTP status code (1xx, 2xx, 3xx, 4xx, 5xx)
// or a range of valid status codes (e.g., "200-299").
func checkStatusCodeRange(code string) (valid, isRange bool) {
	if statusCodeRegex.MatchString(code) {
		return true, strings.Contains(code, "-")
	}
	return false, false
}

func (v *LoadBalancerCustomValidator) usedFloatingIPs(ctx context.Context) (map[string]types.NamespacedName, error) {
	usedIPs := make(map[string]types.NamespacedName)

	var lbList cloudscalev1beta1.LoadBalancerList
	if err := v.Client.List(ctx, &lbList); err != nil {
		return nil, fmt.Errorf("failed to list LoadBalancers: %w", err)
	}

	for _, lb := range lbList.Items {
		for _, fip := range lb.Spec.FloatingIPAddresses {
			if fip.CIDR != "" {
				usedIPs[fip.CIDR] = types.NamespacedName{
					Name:      lb.Name,
					Namespace: lb.Namespace,
				}
			}
		}
	}

	return usedIPs, nil
}

func validationErrorsToError(errs []string) error {
	if len(errs) == 0 {
		return nil
	}

	msg := strings.Builder{}
	msg.WriteString("validation errors: \n")
	for _, err := range errs {
		msg.WriteString(fmt.Sprintf("* %s\n", err))
	}
	return errors.New(msg.String())
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type LoadBalancer.
func (v *LoadBalancerCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}
