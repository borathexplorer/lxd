package cluster_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/client"
	"github.com/canonical/lxd/lxd/cluster"
	"github.com/canonical/lxd/lxd/db"
	"github.com/canonical/lxd/lxd/node"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/lxd/util"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
)

// The returned notifier connects to all nodes.
func TestNewNotifier(t *testing.T) {
	state, cleanup := state.NewTestState(t)
	defer cleanup()

	cert := shared.TestingKeyPair()

	f := notifyFixtures{t: t, state: state}
	cleanupF := f.Nodes(cert, 3)
	defer cleanupF()

	// Populate state.LocalConfig after nodes created above.
	var err error
	var nodeConfig *node.Config
	err = state.DB.Node.Transaction(context.TODO(), func(ctx context.Context, tx *db.NodeTx) error {
		nodeConfig, err = node.ConfigLoad(ctx, tx)
		return err
	})
	require.NoError(t, err)

	state.LocalConfig = nodeConfig

	notifier, err := cluster.NewNotifier(state, cert, cert, cluster.NotifyAll)
	require.NoError(t, err)

	peers := make(chan string, 2)
	hook := func(member db.NodeInfo, client lxd.InstanceServer) error {
		server, _, err := client.GetServer()
		require.NoError(t, err)
		address, ok := server.Config["cluster.https_address"].(string)
		require.True(t, ok)
		peers <- address
		return nil
	}

	assert.NoError(t, notifier(hook))

	addresses := make([]string, 2)
	for i := range addresses {
		select {
		case addresses[i] = <-peers:
		default:
		}
	}
	require.NoError(t, err)
	for i := range addresses {
		assert.True(t, slices.Contains(addresses, f.Address(i+1)))
	}
}

// Creating a new notifier fails if the policy is set to NotifyAll and one of
// the nodes is down.
func TestNewNotify_NotifyAllError(t *testing.T) {
	state, cleanup := state.NewTestState(t)
	defer cleanup()

	cert := shared.TestingKeyPair()

	f := notifyFixtures{t: t, state: state}
	cleanupF := f.Nodes(cert, 3)
	defer cleanupF()

	f.Down(1)

	// Populate state.LocalConfig after nodes created above.
	var err error
	var nodeConfig *node.Config
	err = state.DB.Node.Transaction(context.TODO(), func(ctx context.Context, tx *db.NodeTx) error {
		nodeConfig, err = node.ConfigLoad(ctx, tx)
		return err
	})
	require.NoError(t, err)

	state.LocalConfig = nodeConfig

	notifier, err := cluster.NewNotifier(state, cert, cert, cluster.NotifyAll)
	assert.Nil(t, notifier)
	require.Error(t, err)
	assert.Regexp(t, "Peer cluster member .+ at .+ is down", err.Error())
}

// Creating a new notifier does not fail if the policy is set to NotifyAlive
// and one of the nodes is down, however dead nodes are ignored.
func TestNewNotify_NotifyAlive(t *testing.T) {
	state, cleanup := state.NewTestState(t)
	defer cleanup()

	cert := shared.TestingKeyPair()

	f := notifyFixtures{t: t, state: state}
	cleanupF := f.Nodes(cert, 3)
	defer cleanupF()

	f.Down(1)

	// Populate state.LocalConfig after nodes created above.
	var err error
	var nodeConfig *node.Config
	err = state.DB.Node.Transaction(context.TODO(), func(ctx context.Context, tx *db.NodeTx) error {
		nodeConfig, err = node.ConfigLoad(ctx, tx)
		return err
	})
	require.NoError(t, err)

	state.LocalConfig = nodeConfig

	notifier, err := cluster.NewNotifier(state, cert, cert, cluster.NotifyAlive)
	assert.NoError(t, err)

	i := 0
	hook := func(member db.NodeInfo, client lxd.InstanceServer) error {
		i++
		return nil
	}

	assert.NoError(t, notifier(hook))
	assert.Equal(t, 1, i)
}

// Creating a new notifier does not fail if the policy is set to NotifyAlive
// and one of the nodes is shutting down.
func TestNewNotify_NotifyAliveShuttingDown(t *testing.T) {
	testState, cleanup := state.NewTestState(t)
	defer cleanup()

	cert := shared.TestingKeyPair()

	f := notifyFixtures{t: t, state: testState}
	cleanupF := f.Nodes(cert, 3)
	defer cleanupF()

	f.Unavailable(1, errors.New("LXD is shutting down"))

	// Populate state.LocalConfig after nodes created above.
	var err error
	var nodeConfig *node.Config
	err = testState.DB.Node.Transaction(context.TODO(), func(ctx context.Context, tx *db.NodeTx) error {
		nodeConfig, err = node.ConfigLoad(ctx, tx)
		return err
	})
	require.NoError(t, err)

	testState.LocalConfig = nodeConfig

	notifier, err := cluster.NewNotifier(testState, cert, cert, cluster.NotifyAlive)
	assert.NoError(t, err)

	connections := 0
	hook := func(member db.NodeInfo, client lxd.InstanceServer) error {
		connections++
		// Notifiers do not GetServer() when they set up the connection;
		_, _, err := client.GetServer()
		return err
	}

	assert.NoError(t, notifier(hook))
	assert.Equal(t, 2, connections)
}

// Helper for setting fixtures for Notify tests.
type notifyFixtures struct {
	t       *testing.T
	state   *state.State
	servers []*httptest.Server
}

// Spawn the given number of fake nodes, save in them in the database and
// return a cleanup function.
//
// The address of the first node spawned will be saved as local
// cluster.https_address.
func (h *notifyFixtures) Nodes(cert *shared.CertInfo, n int) func() {
	if n > 1 {
		h.state.ServerClustered = true
		h.state.ServerName = "0"
	}

	servers := make([]*httptest.Server, n)
	for i := range n {
		servers[i] = newRestServer(strconv.Itoa(i), cert)
	}

	// Insert new entries in the nodes table of the cluster database.
	err := h.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		for i := range n {
			name := strconv.Itoa(i)
			address := servers[i].Listener.Addr().String()
			var err error
			if i == 0 {
				err = tx.BootstrapNode(name, address)
			} else {
				_, err = tx.CreateNode(name, address)
			}

			require.NoError(h.t, err)
		}

		return nil
	})
	require.NoError(h.t, err)

	// Set the address in the config table of the node database.
	err = h.state.DB.Node.Transaction(context.Background(), func(ctx context.Context, tx *db.NodeTx) error {
		config, err := node.ConfigLoad(ctx, tx)
		require.NoError(h.t, err)
		address := servers[0].Listener.Addr().String()
		values := map[string]any{"cluster.https_address": address}
		_, err = config.Patch(values)
		require.NoError(h.t, err)
		return nil
	})
	require.NoError(h.t, err)

	cleanup := func() {
		for _, server := range servers {
			server.Close()
		}
	}

	h.servers = servers

	return cleanup
}

// Return the network address of the i-th node.
func (h *notifyFixtures) Address(i int) string {
	var address string
	err := h.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		members, err := tx.GetNodes(ctx)
		require.NoError(h.t, err)
		address = members[i].Address
		return nil
	})
	require.NoError(h.t, err)
	return address
}

// Mark the i'th node as down.
func (h *notifyFixtures) Down(i int) {
	err := h.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		members, err := tx.GetNodes(ctx)
		require.NoError(h.t, err)
		err = tx.SetNodeHeartbeat(members[i].Address, time.Now().Add(-time.Minute))
		require.NoError(h.t, err)
		return nil
	})
	require.NoError(h.t, err)
	h.servers[i].Close()
}

func (h *notifyFixtures) Unavailable(i int, err error) {
	mux := http.NewServeMux()

	mux.HandleFunc("/1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := response.Unavailable(err)
		_ = err.Render(w, r)
	})

	h.servers[i].Config.Handler = mux
}

// Returns a minimal stub for the LXD RESTful API server, just realistic
// enough to make lxd.ConnectLXD succeed.
func newRestServer(name string, cert *shared.CertInfo) *httptest.Server {
	mux := http.NewServeMux()

	server := httptest.NewUnstartedServer(mux)
	server.TLS = util.ServerTLSConfig(cert)
	server.StartTLS()

	mux.HandleFunc("/1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		config := map[string]any{"cluster.https_address": server.Listener.Addr().String()}
		metadata := api.ServerPut{Config: config}
		_ = util.WriteJSON(w, api.ResponseRaw{Metadata: metadata}, nil)
	})

	mux.HandleFunc(fmt.Sprintf("/1.0/cluster/members/%s/state", name), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		metadata := api.ClusterMemberState{
			SysInfo: api.ClusterMemberSysInfo{
				LogicalCPUs: 24,
			},
		}

		_ = util.WriteJSON(w, api.ResponseRaw{Metadata: metadata}, nil)
	})

	return server
}
