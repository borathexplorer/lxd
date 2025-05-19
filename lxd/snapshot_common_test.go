package main

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
)

func (suite *containerTestSuite) TestSnapshotScheduling() {
	args := db.InstanceArgs{
		Type:      instancetype.Container,
		Ephemeral: false,
		Name:      "hal9000",
	}

	c, op, _, err := instance.CreateInternal(suite.d.State(), args, true)
	suite.Req.Nil(err)
	suite.True(snapshotIsScheduledNow("* * * * *",
		int64(c.ID())),
		"snapshot.schedule config '* * * * *' should have matched now")
	suite.True(snapshotIsScheduledNow("@daily,"+
		"@hourly,"+
		"@midnight,"+
		"@weekly,"+
		"@monthly,"+
		"@annually,"+
		"@yearly,"+
		" * * * * *",
		int64(c.ID())),
		"snapshot.schedule config '* * * * *' should have matched now")
	op.Done(nil)
}

func TestSnapshotCommon(t *testing.T) {
	suite.Run(t, new(containerTestSuite))
}
