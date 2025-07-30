package main

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/gorilla/mux"

	"github.com/canonical/lxd/lxd/auth"
	"github.com/canonical/lxd/lxd/db/cluster"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/project"
	"github.com/canonical/lxd/lxd/request"
	"github.com/canonical/lxd/lxd/response"
	storagePools "github.com/canonical/lxd/lxd/storage"
	storageDrivers "github.com/canonical/lxd/lxd/storage/drivers"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/entity"
)

var storagePoolVolumeTypeStateCmd = APIEndpoint{
	Path:        "storage-pools/{poolName}/volumes/{type}/{volumeName}/state",
	MetricsType: entity.TypeStoragePool,

	Get: APIEndpointAction{Handler: storagePoolVolumeTypeStateGet, AccessHandler: allowPermission(entity.TypeStorageVolume, auth.EntitlementCanView, "poolName", "type", "volumeName")},
}

// swagger:operation GET /1.0/storage-pools/{poolName}/volumes/{type}/{volumeName}/state storage storage_pool_volume_type_state_get
//
//	Get the storage volume state
//
//	Gets a specific storage volume state (usage data).
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
//	    name: target
//	    description: Cluster member name
//	    type: string
//	    example: lxd01
//	responses:
//	  "200":
//	    description: Storage pool
//	    schema:
//	      type: object
//	      description: Sync response
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
//	          $ref: "#/definitions/StorageVolumeState"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func storagePoolVolumeTypeStateGet(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	target := request.QueryParam(r, "target")
	resp := forwardedResponseToNode(r.Context(), s, target)
	if resp != nil {
		return resp
	}

	// Get the name of the pool the storage volume is supposed to be attached to.
	poolName, err := url.PathUnescape(mux.Vars(r)["poolName"])
	if err != nil {
		return response.SmartError(err)
	}

	// Get the name of the volume type.
	volumeTypeName, err := url.PathUnescape(mux.Vars(r)["type"])
	if err != nil {
		return response.SmartError(err)
	}

	// Get the name of the volume type.
	volumeName, err := url.PathUnescape(mux.Vars(r)["volumeName"])
	if err != nil {
		return response.SmartError(err)
	}

	// Convert the volume type name to our internal integer representation.
	volumeType, err := cluster.StoragePoolVolumeTypeFromName(volumeTypeName)
	if err != nil {
		return response.BadRequest(err)
	}

	// Check that the storage volume type is valid.
	if !slices.Contains([]cluster.StoragePoolVolumeType{cluster.StoragePoolVolumeTypeCustom, cluster.StoragePoolVolumeTypeContainer, cluster.StoragePoolVolumeTypeVM}, volumeType) {
		return response.BadRequest(fmt.Errorf("Invalid storage volume type %q", volumeTypeName))
	}

	// Get the storage project name.
	requestProjectName := request.ProjectParam(r)
	projectName, err := project.StorageVolumeProject(s.DB.Cluster, requestProjectName, volumeType)
	if err != nil {
		return response.SmartError(err)
	}

	// Load the storage pool.
	pool, err := storagePools.LoadByName(s, poolName)
	if err != nil {
		return response.SmartError(err)
	}

	// Fetch the current usage.
	var usage *storagePools.VolumeUsage
	if volumeType == cluster.StoragePoolVolumeTypeCustom {
		// Custom volumes.
		usage, err = pool.GetCustomVolumeUsage(projectName, volumeName)
		if err != nil && err != storageDrivers.ErrNotSupported {
			return response.SmartError(err)
		}
	} else {
		resp, err := forwardedResponseIfInstanceIsRemote(r.Context(), s, projectName, volumeName, instancetype.Any)
		if err != nil {
			return response.SmartError(err)
		}

		if resp != nil {
			return resp
		}

		// Instance volumes.
		inst, err := instance.LoadByProjectAndName(s, projectName, volumeName)
		if err != nil {
			return response.SmartError(err)
		}

		usage, err = pool.GetInstanceUsage(inst)
		if err != nil && err != storageDrivers.ErrNotSupported {
			return response.SmartError(err)
		}
	}

	// Prepare the state struct.
	state := api.StorageVolumeState{}

	if usage != nil {
		state.Usage = &api.StorageVolumeStateUsage{}

		// Only fill 'used' field if receiving a valid value.
		if usage.Used >= 0 {
			state.Usage.Used = uint64(usage.Used)
		}

		// Only fill 'total' field if receiving a valid value.
		if usage.Total >= 0 {
			state.Usage.Total = usage.Total
		}
	}

	return response.SyncResponse(true, state)
}
