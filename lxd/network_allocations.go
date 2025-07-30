package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/canonical/lxd/lxd/auth"
	clusterRequest "github.com/canonical/lxd/lxd/cluster/request"
	"github.com/canonical/lxd/lxd/db"
	dbCluster "github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/network"
	"github.com/canonical/lxd/lxd/project"
	"github.com/canonical/lxd/lxd/request"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/entity"
	"github.com/canonical/lxd/shared/version"
)

var networkAllocationsCmd = APIEndpoint{
	Path:        "network-allocations",
	MetricsType: entity.TypeNetwork,

	Get: APIEndpointAction{Handler: networkAllocationsGet, AccessHandler: allowProjectResourceList},
}

// swagger:operation GET /1.0/network-allocations network-allocations network_allocations_get
//
//	Get the network allocations in use (`network`, `network-forward`, `load-balancer`, `uplink` and `instance`)
//
//	Returns a list of network allocations in use by a LXD deployment.
//
//	---
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	  - in: query
//	    name: all-projects
//	    description: Retrieve entities from all projects
//	    type: boolean
//	responses:
//	  "200":
//	    description: API endpoints
//	    schema:
//	      type: object
//	      properties:
//	        type:
//	          type: string
//	          description: Response type
//	          example: sync
//	        status:
//	          type: string
//	          description: Status description
//	          example: Success
//	        status_code:
//	          type: integer
//	          description: Status code
//	          example: 200
//	        metadata:
//	          type: array
//	          description: List of network allocations used by a consuming entity
//	          items:
//	            $ref: "#/definitions/NetworkAllocations"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func networkAllocationsGet(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	requestProjectName := request.ProjectParam(r)
	effectiveProjectName, _, err := project.NetworkProject(d.State().DB.Cluster, requestProjectName)
	if err != nil {
		return response.SmartError(err)
	}

	reqInfo := request.SetupContextInfo(r)
	reqInfo.EffectiveProjectName = effectiveProjectName

	allProjects := shared.IsTrue(request.QueryParam(r, "all-projects"))

	var projectNames []string
	err = d.db.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
		// Figure out the projects to retrieve.
		if !allProjects {
			projectNames = []string{effectiveProjectName}
		} else {
			// Get all project names if no specific project requested.
			projectNames, err = dbCluster.GetProjectNames(ctx, tx.Tx())
			if err != nil {
				return fmt.Errorf("Failed loading projects: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return response.SmartError(err)
	}

	// Helper function to get the CIDR address of an IP (/32 or /128 mask for ipv4 or ipv6 respectively).
	// Returns IP address in its canonical CIDR form and whether the network is using NAT for that IP family.
	ipToCIDR := func(addr string, netConf map[string]string) (string, bool, error) {
		ip := net.ParseIP(addr)
		if ip == nil {
			return "", false, fmt.Errorf("Invalid IP address %q", addr)
		}

		if ip.To4() != nil {
			return ip.String() + "/32", shared.IsTrue(netConf["ipv4.nat"]), nil
		}

		return ip.String() + "/128", shared.IsTrue(netConf["ipv6.nat"]), nil
	}

	result := make([]api.NetworkAllocations, 0)

	canViewNetwork, err := s.Authorizer.GetPermissionChecker(r.Context(), auth.EntitlementCanView, entity.TypeNetwork)
	if err != nil {
		return response.SmartError(err)
	}

	// If project "foo" is provided but "foo" has `features.networks=false`, then we'll be returning IP allocations
	// from the default project. In this case, "default" is set as the effective project on the request.Info in the
	// context of the auth check. This tells the fine-grained auth driver to overwrite "foo" in the URL with "default"
	// so it can find the actual entity.
	//
	// When performing auth checks for instances, the network feature has no relevance, so we need the authorizer to
	// ignore the effective project. If we didn't do this, URLs would have the project parameter rewritten to "default"
	// and the authorizer would either not be able to find an entity with that URL, or perform an auth check against an
	// incorrect entity.
	canViewInstanceIgnoringEffectiveProject, err := s.Authorizer.GetPermissionCheckerWithoutEffectiveProject(r.Context(), auth.EntitlementCanView, entity.TypeInstance)
	if err != nil {
		return response.SmartError(err)
	}

	// Then, get all the networks, their network forwards and their network load balancers.
	for _, projectName := range projectNames {
		// The auth.PermissionChecker expects the url to contain the request project (not the effective project).
		// So when getting networks in a single project, ensure we use the request project name.
		authCheckProjectName := projectName
		if !allProjects {
			authCheckProjectName = requestProjectName
		}

		var networkNames []string

		err := d.db.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
			var err error

			networkNames, err = tx.GetNetworks(ctx, projectName)

			return err
		})
		if err != nil {
			return response.SmartError(fmt.Errorf("Failed loading networks: %w", err))
		}

		// Get all the networks, their attached instances, their network forwards and their network load balancers.
		for _, networkName := range networkNames {
			if !canViewNetwork(entity.NetworkURL(authCheckProjectName, networkName)) {
				continue
			}

			n, err := network.LoadByName(d.State(), projectName, networkName)
			if err != nil {
				return response.SmartError(fmt.Errorf("Failed loading network %q in project %q: %w", networkName, projectName, err))
			}

			netConf := n.Config()

			for _, keyPrefix := range []string{"ipv4", "ipv6"} {
				ipNet, _ := network.ParseIPCIDRToNet(netConf[keyPrefix+".address"])
				if ipNet == nil {
					continue
				}

				result = append(result, api.NetworkAllocations{
					Address: ipNet.String(),
					UsedBy:  api.NewURL().Path(version.APIVersion, "networks", networkName).Project(projectName).String(),
					Type:    "network",
					NAT:     shared.IsTrue(netConf[keyPrefix+".nat"]),
					Network: networkName,
				})
			}

			leases, err := n.Leases("", clusterRequest.ClientTypeNormal)
			if err != nil && !errors.Is(err, network.ErrNotImplemented) {
				return response.SmartError(fmt.Errorf("Failed getting leases for network %q: %w", networkName, err))
			}

			leaseTypes := []string{"static", "dynamic", "uplink"}
			for _, lease := range leases {
				if slices.Contains(leaseTypes, lease.Type) {
					cidrAddr, nat, err := ipToCIDR(lease.Address, netConf)
					if err != nil {
						return response.SmartError(err)
					}

					var allocationType, usedBy string
					if lease.Type == "uplink" {
						allocationType = "uplink"
						networkName := strings.TrimSuffix(strings.TrimPrefix(lease.Hostname, lease.Project+"-"), ".uplink")
						usedByURL := api.NewURL().Path(version.APIVersion, "networks", networkName).Project(lease.Project)
						if !canViewNetwork(usedByURL) {
							continue
						}

						usedBy = usedByURL.String()
					} else {
						allocationType = "instance"
						usedByURL := api.NewURL().Path(version.APIVersion, "instances", lease.Hostname).Project(lease.Project)
						if !canViewInstanceIgnoringEffectiveProject(usedByURL) {
							continue
						}

						usedBy = usedByURL.String()
					}

					result = append(result, api.NetworkAllocations{
						Address: cidrAddr,
						UsedBy:  usedBy,
						Type:    allocationType,
						Hwaddr:  lease.Hwaddr,
						NAT:     nat,
						Network: networkName,
					})
				}
			}

			var forwards map[int64]*api.NetworkForward

			err = d.db.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
				forwards, err = tx.GetNetworkForwards(ctx, n.ID(), false)

				return err
			})
			if err != nil {
				return response.SmartError(fmt.Errorf("Failed getting forwards for network %q in project %q: %w", networkName, projectName, err))
			}

			for _, forward := range forwards {
				cidrAddr, _, err := ipToCIDR(forward.ListenAddress, netConf)
				if err != nil {
					return response.SmartError(err)
				}

				result = append(
					result,
					api.NetworkAllocations{
						Address: cidrAddr,
						// No auth check here, the caller can view the network forward because they can view the network.
						UsedBy:  api.NewURL().Path(version.APIVersion, "networks", networkName, "forwards", forward.ListenAddress).Project(projectName).String(),
						Type:    "network-forward",
						NAT:     false, // Network forwards are ingress and so aren't affected by SNAT.
						Network: networkName,
					},
				)
			}

			var loadBalancers map[int64]*api.NetworkLoadBalancer

			err = d.db.Cluster.Transaction(r.Context(), func(ctx context.Context, tx *db.ClusterTx) error {
				loadBalancers, err = tx.GetNetworkLoadBalancers(ctx, n.ID(), false)

				return err
			})
			if err != nil {
				return response.SmartError(fmt.Errorf("Failed getting load-balancers for network %q in project %q: %w", networkName, projectName, err))
			}

			for _, loadBalancer := range loadBalancers {
				cidrAddr, _, err := ipToCIDR(loadBalancer.ListenAddress, netConf)
				if err != nil {
					return response.SmartError(err)
				}

				result = append(
					result,
					api.NetworkAllocations{
						Address: cidrAddr,
						// No auth check here, the caller can view the load balancer because they can view the network.
						UsedBy:  api.NewURL().Path(version.APIVersion, "networks", networkName, "load-balancers", loadBalancer.ListenAddress).Project(projectName).String(),
						Type:    "network-load-balancer",
						NAT:     false, // Network load-balancers are ingress and so aren't affected by SNAT.
						Network: networkName,
					},
				)
			}
		}
	}

	return response.SyncResponse(true, result)
}
