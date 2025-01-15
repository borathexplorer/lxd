//go:build linux && cgo && !agent

package cluster

import (
	"time"

	"github.com/canonical/lxd/lxd/db/warningtype"
	"github.com/canonical/lxd/shared/api"
)

// Code generation directives.
//
//go:generate -command mapper lxd-generate db mapper -t warnings.mapper.go
//go:generate mapper reset -i -b "//go:build linux && cgo && !agent"
//
//go:generate mapper stmt -e warning objects
//go:generate mapper stmt -e warning objects-by-UUID
//go:generate mapper stmt -e warning objects-by-Project
//go:generate mapper stmt -e warning objects-by-Status
//go:generate mapper stmt -e warning objects-by-Node-and-Status
//go:generate mapper stmt -e warning objects-by-Node-and-TypeCode
//go:generate mapper stmt -e warning objects-by-Node-and-TypeCode-and-Project
//go:generate mapper stmt -e warning objects-by-Node-and-TypeCode-and-Project-and-EntityType-and-EntityID
//go:generate mapper stmt -e warning delete-by-UUID
//go:generate mapper stmt -e warning delete-by-EntityType-and-EntityID
//go:generate mapper stmt -e warning id
//
//go:generate mapper method -i -e warning GetMany
//go:generate mapper method -i -e warning GetOne-by-UUID
//go:generate mapper method -i -e warning DeleteOne-by-UUID
//go:generate mapper method -i -e warning DeleteMany-by-EntityType-and-EntityID
//go:generate mapper method -i -e warning ID
//go:generate mapper method -i -e warning Exists struct=Warning
//go:generate goimports -w warnings.mapper.go
//go:generate goimports -w warnings.interface.mapper.go

// Warning is a value object holding db-related details about a warning.
type Warning struct {
	ID            int
	Node          string     `db:"coalesce=''&leftjoin=nodes.name"`
	Project       string     `db:"coalesce=''&leftjoin=projects.name"`
	EntityType    EntityType `db:"coalesce=-1&sql=warnings.entity_type_code"`
	EntityID      int        `db:"coalesce=-1"`
	UUID          string     `db:"primary=yes"`
	TypeCode      warningtype.Type
	Status        warningtype.Status
	FirstSeenDate time.Time
	LastSeenDate  time.Time
	UpdatedDate   time.Time
	LastMessage   string
	Count         int
}

// WarningFilter specifies potential query parameter fields.
type WarningFilter struct {
	ID         *int
	UUID       *string
	Project    *string
	Node       *string
	TypeCode   *warningtype.Type
	EntityType *EntityType
	EntityID   *int
	Status     *warningtype.Status
}

// ToAPI returns a LXD API entry.
func (w Warning) ToAPI() api.Warning {
	typeCode := warningtype.Type(w.TypeCode)

	return api.Warning{
		UUID:        w.UUID,
		Location:    w.Node,
		Project:     w.Project,
		Type:        warningtype.TypeNames[typeCode],
		Count:       w.Count,
		FirstSeenAt: w.FirstSeenDate,
		LastSeenAt:  w.LastSeenDate,
		LastMessage: w.LastMessage,
		Severity:    warningtype.Severities[typeCode.Severity()],
		Status:      warningtype.Statuses[warningtype.Status(w.Status)],
	}
}
