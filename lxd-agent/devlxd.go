package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/canonical/lxd/client"
	"github.com/canonical/lxd/lxd/device/config"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/version"
)

// devLXDAPIHandler is a function that handles requests to the DevLXD API.
type devLXDHandlerFunc func(d *Daemon, r *http.Request) *devLXDResponse

// devLXDAPIEndpointAction represents an action on an devlxd API endpoint.
type devLXDAPIEndpointAction struct {
	Handler devLXDHandlerFunc
}

// devLXDAPIEndpoint represents a URL in devLXD API.
type devLXDAPIEndpoint struct {
	Name   string // Name for this endpoint.
	Path   string // Path pattern for this endpoint
	Get    devLXDAPIEndpointAction
	Head   devLXDAPIEndpointAction
	Put    devLXDAPIEndpointAction
	Post   devLXDAPIEndpointAction
	Delete devLXDAPIEndpointAction
	Patch  devLXDAPIEndpointAction
}

var devLXDEndpoints = []devLXDAPIEndpoint{
	{
		Path: "/",
		Get: devLXDAPIEndpointAction{
			Handler: func(d *Daemon, r *http.Request) *devLXDResponse {
				return okResponse([]string{"/1.0"}, "json")
			},
		},
	},
	devLXD10Endpoint,
	devLXDConfigEndpoint,
	devLXDConfigKeyEndpoint,
	devLXDMetadataEndpoint,
	devLXDEventsEndpoint,
	devLXDDevicesEndpoint,
	devLXDImageExportEndpoint,
	devLXDUbuntuProEndpoint,
	devLXDUbuntuProTokenEndpoint,
}

// devLxdServer creates an http.Server capable of handling requests against the
// /dev/lxd Unix socket endpoint created inside VMs.
func devLXDServer(d *Daemon) *http.Server {
	return &http.Server{
		Handler: devLXDAPI(d),
	}
}

func getVsockClient(d *Daemon) (lxd.InstanceServer, error) {
	// Try connecting to LXD server.
	client, err := getClient(d.serverCID, int(d.serverPort), d.serverCertificate)
	if err != nil {
		return nil, err
	}

	server, err := lxd.ConnectLXDHTTP(nil, client)
	if err != nil {
		return nil, err
	}

	return server, nil
}

var devLXD10Endpoint = devLXDAPIEndpoint{
	Path:  "",
	Get:   devLXDAPIEndpointAction{Handler: devLXDAPIGetHandler},
	Patch: devLXDAPIEndpointAction{Handler: devLXDAPIPatchHandler},
}

func devLXDAPIGetHandler(d *Daemon, r *http.Request) *devLXDResponse {
	client, err := getVsockClient(d)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	defer client.Disconnect()

	resp, _, err := client.RawQuery(r.Method, "/1.0", nil, "")
	if err != nil {
		return smartResponse(err)
	}

	var instanceData api.DevLXDGet

	err = resp.MetadataAsStruct(&instanceData)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed parsing response from LXD: %w", err))
	}

	return okResponse(instanceData, "json")
}

func devLXDAPIPatchHandler(d *Daemon, r *http.Request) *devLXDResponse {
	client, err := getVsockClient(d)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	defer client.Disconnect()

	_, _, err = client.RawQuery(r.Method, "/1.0", r.Body, "")
	if err != nil {
		return smartResponse(err)
	}

	return okResponse("", "raw")
}

var devLXDConfigEndpoint = devLXDAPIEndpoint{
	Path: "config",
	Get:  devLXDAPIEndpointAction{Handler: devLXDConfigGetHandler},
}

func devLXDConfigGetHandler(d *Daemon, r *http.Request) *devLXDResponse {
	client, err := getVsockClient(d)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	defer client.Disconnect()

	resp, _, err := client.RawQuery("GET", "/1.0/config", nil, "")
	if err != nil {
		return smartResponse(err)
	}

	var config []string

	err = resp.MetadataAsStruct(&config)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed parsing response from LXD: %w", err))
	}

	filtered := []string{}
	for _, k := range config {
		if strings.HasPrefix(k, "/1.0/config/user.") || strings.HasPrefix(k, "/1.0/config/cloud-init.") {
			filtered = append(filtered, k)
		}
	}

	return okResponse(filtered, "json")
}

var devLXDConfigKeyEndpoint = devLXDAPIEndpoint{
	Path: "config/{key}",
	Get:  devLXDAPIEndpointAction{Handler: devLXDConfigKeyGetHandler},
}

func devLXDConfigKeyGetHandler(d *Daemon, r *http.Request) *devLXDResponse {
	key, err := url.PathUnescape(mux.Vars(r)["key"])
	if err != nil {
		return errorResponse(http.StatusBadRequest, "bad request")
	}

	if !strings.HasPrefix(key, "user.") && !strings.HasPrefix(key, "cloud-init.") {
		return errorResponse(http.StatusForbidden, "not authorized")
	}

	client, err := getVsockClient(d)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	defer client.Disconnect()

	resp, _, err := client.RawQuery("GET", "/1.0/config/"+key, nil, "")
	if err != nil {
		return smartResponse(err)
	}

	var value string

	err = resp.MetadataAsStruct(&value)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed parsing response from LXD: %w", err))
	}

	return okResponse(value, "raw")
}

var devLXDMetadataEndpoint = devLXDAPIEndpoint{
	Path: "meta-data",
	Get:  devLXDAPIEndpointAction{Handler: devLXDMetadataGetHandler},
}

func devLXDMetadataGetHandler(d *Daemon, r *http.Request) *devLXDResponse {
	var client lxd.InstanceServer
	var err error

	for range 10 {
		client, err = getVsockClient(d)
		if err == nil {
			break
		}

		time.Sleep(500 * time.Millisecond)
	}

	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	defer client.Disconnect()

	resp, _, err := client.RawQuery("GET", "/1.0/meta-data", nil, "")
	if err != nil {
		return smartResponse(err)
	}

	var metaData string

	err = resp.MetadataAsStruct(&metaData)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed parsing response from LXD: %w", err))
	}

	return okResponse(metaData, "raw")
}

var devLXDEventsEndpoint = devLXDAPIEndpoint{
	Path: "events",
	Get:  devLXDAPIEndpointAction{Handler: devLXDEventsGetHandler},
}

func devLXDEventsGetHandler(d *Daemon, r *http.Request) *devLXDResponse {
	return manualResponse(func(w http.ResponseWriter) error {
		err := eventsGet(d, r).Render(w, r)
		if err != nil {
			return err
		}

		return nil
	})
}

var devLXDDevicesEndpoint = devLXDAPIEndpoint{
	Path: "devices",
	Get:  devLXDAPIEndpointAction{Handler: devLXDDevicesGetHandler},
}

func devLXDDevicesGetHandler(d *Daemon, r *http.Request) *devLXDResponse {
	client, err := getVsockClient(d)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	defer client.Disconnect()

	resp, _, err := client.RawQuery("GET", "/1.0/devices", nil, "")
	if err != nil {
		return smartResponse(err)
	}

	var devices config.Devices

	err = resp.MetadataAsStruct(&devices)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed parsing response from LXD: %w", err))
	}

	return okResponse(devices, "json")
}

var devLXDImageExportEndpoint = devLXDAPIEndpoint{
	Path: "images/{fingerprint}/export",
	Get:  devLXDAPIEndpointAction{Handler: devLXDImageExportHandler},
}

func devLXDImageExportHandler(d *Daemon, r *http.Request) *devLXDResponse {
	// Extract the fingerprint.
	fingerprint, err := url.PathUnescape(mux.Vars(r)["fingerprint"])
	if err != nil {
		return smartResponse(err)
	}

	// Get a http.Client.
	client, err := getClient(d.serverCID, int(d.serverPort), d.serverCertificate)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	// Remove the request URI, this cannot be set on requests.
	r.RequestURI = ""

	// Set up the request URL with the correct host.
	r.URL = &api.NewURL().Scheme("https").Host("custom.socket").Path(version.APIVersion, "images", fingerprint, "export").URL

	// Proxy the request.
	resp, err := client.Do(r)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, err.Error())
	}

	return manualResponse(func(w http.ResponseWriter) error {
		// Set headers from the host LXD.
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Set(k, v)
			}
		}

		// Copy headers and response body.
		w.WriteHeader(resp.StatusCode)
		_, err = io.Copy(w, resp.Body)
		if err != nil {
			return err
		}

		return nil
	})
}

var devLXDUbuntuProEndpoint = devLXDAPIEndpoint{
	Path: "ubuntu-pro",
	Get:  devLXDAPIEndpointAction{Handler: devLXDUbuntuProGetHandler},
}

func devLXDUbuntuProGetHandler(d *Daemon, r *http.Request) *devLXDResponse {
	// Get a http.Client.
	client, err := getClient(d.serverCID, int(d.serverPort), d.serverCertificate)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	// Remove the request URI, this cannot be set on requests.
	r.RequestURI = ""

	// Set up the request URL with the correct host.
	r.URL = &api.NewURL().Scheme("https").Host("custom.socket").Path(version.APIVersion, "ubuntu-pro").URL

	// Proxy the request.
	resp, err := client.Do(r)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, err.Error())
	}

	var apiResponse api.Response
	err = json.NewDecoder(resp.Body).Decode(&apiResponse)
	if err != nil {
		return smartResponse(err)
	}

	var settingsResponse api.UbuntuProSettings
	err = json.Unmarshal(apiResponse.Metadata, &settingsResponse)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, fmt.Sprintf("Invalid Ubuntu Token settings response received from host: %v", err))
	}

	return okResponse(settingsResponse, "json")
}

var devLXDUbuntuProTokenEndpoint = devLXDAPIEndpoint{
	Path: "/ubuntu-pro/token",
	Post: devLXDAPIEndpointAction{Handler: devLXDUbuntuProTokenPostHandler},
}

func devLXDUbuntuProTokenPostHandler(d *Daemon, r *http.Request) *devLXDResponse {
	// Get a http.Client.
	client, err := getClient(d.serverCID, int(d.serverPort), d.serverCertificate)
	if err != nil {
		return smartResponse(fmt.Errorf("Failed connecting to LXD over vsock: %w", err))
	}

	// Remove the request URI, this cannot be set on requests.
	r.RequestURI = ""

	// Set up the request URL with the correct host.
	r.URL = &api.NewURL().Scheme("https").Host("custom.socket").Path(version.APIVersion, "ubuntu-pro", "token").URL

	// Proxy the request.
	resp, err := client.Do(r)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, err.Error())
	}

	var apiResponse api.Response
	err = json.NewDecoder(resp.Body).Decode(&apiResponse)
	if err != nil {
		return smartResponse(err)
	}

	if apiResponse.StatusCode != http.StatusOK {
		return errorResponse(apiResponse.Code, apiResponse.Error)
	}

	var tokenResponse api.UbuntuProGuestTokenResponse
	err = json.Unmarshal(apiResponse.Metadata, &tokenResponse)
	if err != nil {
		return errorResponse(http.StatusInternalServerError, fmt.Sprintf("Invalid Ubuntu Token response received from host: %v", err))
	}

	return okResponse(tokenResponse, "json")
}

func devLXDAPI(d *Daemon) http.Handler {
	m := mux.NewRouter()
	m.UseEncodedPath() // Allow encoded values in path segments.

	for _, ep := range devLXDEndpoints {
		registerDevLXDEndpoint(d, m, "1.0", ep)
	}

	return m
}

func registerDevLXDEndpoint(d *Daemon, apiRouter *mux.Router, apiVersion string, ep devLXDAPIEndpoint) {
	uri := ep.Path
	if uri != "/" {
		uri = path.Join("/", apiVersion, ep.Path)
	}

	// Function that handles the request by calling the appropriate handler.
	handleFunc := func(w http.ResponseWriter, r *http.Request) {
		handleRequest := func(action devLXDAPIEndpointAction) (resp *devLXDResponse) {
			// Handle panic in the handler.
			defer func() {
				err := recover()
				if err != nil {
					logger.Error("Panic in LXD Agent devLXD API handler", logger.Ctx{"err": err})
					resp = errorResponse(http.StatusInternalServerError, fmt.Sprintf("%v", err))
				}
			}()

			// Verify handler.
			if action.Handler == nil {
				return errorResponse(http.StatusNotImplemented, "")
			}

			return action.Handler(d, r)
		}

		var resp *devLXDResponse

		switch r.Method {
		case http.MethodHead:
			resp = handleRequest(ep.Head)
		case http.MethodGet:
			resp = handleRequest(ep.Get)
		case http.MethodPost:
			resp = handleRequest(ep.Post)
		case http.MethodPut:
			resp = handleRequest(ep.Put)
		case http.MethodPatch:
			resp = handleRequest(ep.Patch)
		case http.MethodDelete:
			resp = handleRequest(ep.Delete)
		default:
			resp = errorResponse(http.StatusNotFound, fmt.Sprintf("Method %q not found", r.Method))
		}

		// Write response.
		err := resp.Render(w, r)
		if err != nil {
			writeErr := errorResponse(http.StatusInternalServerError, err.Error()).Render(w, r)
			if writeErr != nil {
				logger.Warn("Failed writing error for HTTP response", logger.Ctx{"url": uri, "err": err, "writeErr": writeErr})
			}
		}
	}

	route := apiRouter.HandleFunc(uri, handleFunc)

	// If the endpoint has a canonical name then record it so it can be used to build URLS
	// and accessed in the context of the request by the handler function.
	if ep.Name != "" {
		route.Name(ep.Name)
	}
}

// Create a new net.Listener bound to the unix socket of the devLXD endpoint.
func createDevLXDListener(dir string) (net.Listener, error) {
	path := filepath.Join(dir, "lxd", "sock")

	err := os.MkdirAll(filepath.Dir(path), 0755)
	if err != nil {
		return nil, err
	}

	// If this socket exists, that means a previous LXD instance died and
	// didn't clean up. We assume that such LXD instance is actually dead
	// if we get this far, since localCreateListener() tries to connect to
	// the actual lxd socket to make sure that it is actually dead. So, it
	// is safe to remove it here without any checks.
	//
	// Also, it would be nice to SO_REUSEADDR here so we don't have to
	// delete the socket, but we can't:
	//   http://stackoverflow.com/questions/15716302/so-reuseaddr-and-af-unix
	//
	// Note that this will force clients to reconnect when LXD is restarted.
	err = socketUnixRemoveStale(path)
	if err != nil {
		return nil, err
	}

	listener, err := socketUnixListen(path)
	if err != nil {
		return nil, err
	}

	err = socketUnixSetPermissions(path, 0600)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	return listener, nil
}

// Remove any stale socket file at the given path.
func socketUnixRemoveStale(path string) error {
	// If there's no socket file at all, there's nothing to do.
	if !shared.PathExists(path) {
		return nil
	}

	logger.Debugf("Detected stale unix socket, deleting")
	err := os.Remove(path)
	if err != nil {
		return fmt.Errorf("could not delete stale local socket: %w", err)
	}

	return nil
}

// Change the file mode of the given unix socket file.
func socketUnixSetPermissions(path string, mode os.FileMode) error {
	err := os.Chmod(path, mode)
	if err != nil {
		return fmt.Errorf("cannot set permissions on local socket: %w", err)
	}

	return nil
}

// Bind to the given unix socket path.
func socketUnixListen(path string) (net.Listener, error) {
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve socket address: %w", err)
	}

	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("cannot bind socket: %w", err)
	}

	return listener, err
}
