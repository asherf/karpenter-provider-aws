/*
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

package status

import (
	"context"
	"fmt"

	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/aws/karpenter-provider-aws/pkg/apis/v1beta1"
	"github.com/aws/karpenter-provider-aws/pkg/providers/launchtemplate"
)

type LaunchTemplate struct {
	launchTemplateProvider launchtemplate.Provider
}

func (lt *LaunchTemplate) Reconcile(ctx context.Context, nodeClass *v1beta1.EC2NodeClass) (reconcile.Result, error) {
	// A NodeClass that use AL2023 requires the cluster CIDR for launching nodes.
	// To allow Karpenter to be used for Non-EKS clusters, resolving the Cluster CIDR
	// will not be done at startup but instead in a reconcile loop.
	if lo.FromPtr(nodeClass.Spec.AMIFamily) == v1beta1.AMIFamilyAL2023 {
		if err := lt.launchTemplateProvider.ResolveClusterCIDR(ctx); err != nil {
			return reconcile.Result{}, fmt.Errorf("unable to detect the cluster CIDR, %w", err)
		}
	}
	return reconcile.Result{}, nil
}