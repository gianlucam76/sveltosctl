/*
Copyright 2023. projectsveltos.io. All rights reserved.

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

package utils

import (
	"context"

	"github.com/go-logr/logr"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
)

// ListRoleRequests returns all current RoleRequests
func (a *k8sAccess) ListRoleRequests(ctx context.Context,
	logger logr.Logger) (*libsveltosv1alpha1.RoleRequestList, error) {

	logger.V(logs.LogDebug).Info("Get all RoleRequests")
	roleRequests := &libsveltosv1alpha1.RoleRequestList{}
	err := a.client.List(ctx, roleRequests)
	return roleRequests, err
}