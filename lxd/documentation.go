package main

import (
	"embed"
	"encoding/json"
	"net/http"

	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/entity"
)

var metadataConfigurationCmd = APIEndpoint{
	Path:        "metadata/configuration",
	MetricsType: entity.TypeServer,

	Get: APIEndpointAction{Handler: metadataConfigurationGet, AllowUntrusted: true},
}

//go:embed metadata/configuration.json
var generatedDoc embed.FS

// swagger:operation GET /1.0/metadata/configuration metadata_configuration_get
//
//	Get the metadata configuration
//
//	Returns the generated LXD metadata configuration in JSON format.
//
//	---
//	produces:
//	  - text/plain
//	responses:
//	  "200":
//	    description: API endpoints
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
//	          $ref: "#/definitions/MetadataConfiguration"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func metadataConfigurationGet(d *Daemon, r *http.Request) response.Response {
	file, err := generatedDoc.ReadFile("metadata/configuration.json")
	if err != nil {
		return response.SmartError(err)
	}

	var data api.MetadataConfiguration
	err = json.Unmarshal(file, &data)
	if err != nil {
		return response.SmartError(err)
	}

	return response.SyncResponse(true, data)
}
