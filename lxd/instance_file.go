package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/pkg/sftp"

	"github.com/canonical/lxd/lxd/instance"
	"github.com/canonical/lxd/lxd/instance/instancetype"
	"github.com/canonical/lxd/lxd/lifecycle"
	"github.com/canonical/lxd/lxd/request"
	"github.com/canonical/lxd/lxd/response"
	"github.com/canonical/lxd/lxd/state"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/revert"
)

func instanceFileHandler(d *Daemon, r *http.Request) response.Response {
	s := d.State()

	projectName := request.ProjectParam(r)
	name, err := url.PathUnescape(mux.Vars(r)["name"])
	if err != nil {
		return response.SmartError(err)
	}

	if shared.IsSnapshot(name) {
		return response.BadRequest(errors.New("Invalid instance name"))
	}

	// Redirect to correct server if needed.
	instanceType, err := urlInstanceTypeDetect(r)
	if err != nil {
		return response.SmartError(err)
	}

	resp, err := forwardedResponseIfInstanceIsRemote(s, r, projectName, name, instanceType)
	if err != nil {
		return response.SmartError(err)
	}

	if resp != nil {
		return resp
	}

	// Load the instance.
	inst, err := instance.LoadByProjectAndName(s, projectName, name)
	if err != nil {
		return response.SmartError(err)
	}

	// Parse and cleanup the path.
	path := r.FormValue("path")
	if path == "" {
		return response.BadRequest(errors.New("Missing path argument"))
	}

	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	switch r.Method {
	case "GET":
		return instanceFileGet(s, inst, path, r)
	case "HEAD":
		return instanceFileHead(inst, path)
	case "POST":
		return instanceFilePost(s, inst, path, r)
	case "DELETE":
		return instanceFileDelete(s, inst, path)
	default:
		return response.NotFound(fmt.Errorf("Method %q not found", r.Method))
	}
}

// swagger:operation GET /1.0/instances/{name}/files instances instance_files_get
//
//	Get a file
//
//	Gets the file content. If it's a directory, a json list of files will be returned instead.
//
//	---
//	produces:
//	  - application/json
//	  - application/octet-stream
//	parameters:
//	  - in: query
//	    name: path
//	    description: Path to the file
//	    type: string
//	    example: default
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	responses:
//	  "200":
//	     description: Raw file or directory listing
//	     headers:
//	       X-LXD-uid:
//	         description: File owner UID
//	         schema:
//	           type: integer
//	       X-LXD-gid:
//	         description: File owner GID
//	         schema:
//	           type: integer
//	       X-LXD-mode:
//	         description: Mode mask
//	         schema:
//	           type: integer
//	       X-LXD-modified:
//	         description: Last modified date
//	         schema:
//	           type: string
//	       X-LXD-type:
//	         description: Type of file (file, symlink or directory)
//	         schema:
//	           type: string
//	     content:
//	       application/octet-stream:
//	         schema:
//	           type: string
//	           example: some-text
//	       application/json:
//	         schema:
//	           type: array
//	           items:
//	             type: string
//	           example: |-
//	             [
//	               "/etc",
//	               "/home"
//	             ]
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "404":
//	    $ref: "#/responses/NotFound"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func instanceFileGet(s *state.State, inst instance.Instance, path string, r *http.Request) response.Response {
	revert := revert.New()
	defer revert.Fail()

	// Get a SFTP client.
	client, err := inst.FileSFTP()
	if err != nil {
		return response.InternalError(err)
	}

	revert.Add(func() { _ = client.Close() })

	// Get the file stats.
	stat, err := client.Lstat(path)
	if err != nil {
		return response.SmartError(err)
	}

	fileType := "file"
	if stat.Mode().IsDir() {
		fileType = "directory"
	} else if stat.Mode()&os.ModeSymlink == os.ModeSymlink {
		fileType = "symlink"
	}

	fs, _ := stat.Sys().(*sftp.FileStat)

	// Prepare the response.
	headers := map[string]string{
		"X-LXD-uid":      fmt.Sprint(fs.UID),
		"X-LXD-gid":      fmt.Sprint(fs.GID),
		"X-LXD-mode":     fmt.Sprintf("%04o", stat.Mode().Perm()),
		"X-LXD-modified": stat.ModTime().UTC().String(),
		"X-LXD-type":     fileType,
	}

	switch fileType {
	case "file":
		// Open the file.
		file, err := client.Open(path)
		if err != nil {
			return response.SmartError(err)
		}

		revert.Add(func() { _ = file.Close() })

		// Setup cleanup logic.
		cleanup := revert.Clone()
		revert.Success()

		// Make a file response struct.
		files := make([]response.FileResponseEntry, 1)
		files[0].Identifier = filepath.Base(path)
		files[0].Filename = filepath.Base(path)
		files[0].File = file
		files[0].FileSize = stat.Size()
		files[0].FileModified = stat.ModTime()
		files[0].Cleanup = func() {
			cleanup.Fail()
		}

		s.Events.SendLifecycle(inst.Project().Name, lifecycle.InstanceFileRetrieved.Event(inst, logger.Ctx{"path": path}))
		return response.FileResponse(files, headers)
	case "symlink":
		// Find symlink target.
		target, err := client.ReadLink(path)
		if err != nil {
			return response.SmartError(err)
		}

		// If not an absolute symlink, need to mangle to something
		// relative to the source path. This is required because there
		// is no sftp function to get the final target path and RealPath doesn't
		// allow specifying the path to resolve from.
		if !strings.HasPrefix(target, "/") {
			target = filepath.Join(filepath.Dir(path), target)
		}

		// Convert to absolute path.
		target, err = client.RealPath(target)
		if err != nil {
			return response.SmartError(err)
		}

		// Make a file response struct.
		files := make([]response.FileResponseEntry, 1)
		files[0].Identifier = filepath.Base(path)
		files[0].Filename = filepath.Base(path)
		files[0].File = bytes.NewReader([]byte(target))
		files[0].FileModified = time.Now()
		files[0].FileSize = int64(len(target))

		s.Events.SendLifecycle(inst.Project().Name, lifecycle.InstanceFileRetrieved.Event(inst, logger.Ctx{"path": path}))
		return response.FileResponse(files, headers)
	case "directory":
		dirEnts := []string{}

		// List the directory.
		entries, err := client.ReadDir(path)
		if err != nil {
			return response.SmartError(err)
		}

		for _, entry := range entries {
			dirEnts = append(dirEnts, entry.Name())
		}

		s.Events.SendLifecycle(inst.Project().Name, lifecycle.InstanceFileRetrieved.Event(inst, logger.Ctx{"path": path}))
		return response.SyncResponseHeaders(true, dirEnts, headers)
	}

	return response.InternalError(fmt.Errorf("Bad file type: %s", fileType))
}

// swagger:operation HEAD /1.0/instances/{name}/files instances instance_files_head
//
//	Get metadata for a file
//
//	Gets the file or directory metadata.
//
//	---
//	parameters:
//	  - in: query
//	    name: path
//	    description: Path to the file
//	    type: string
//	    example: default
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	responses:
//	  "200":
//	     description: Raw file or directory listing
//	     headers:
//	       X-LXD-uid:
//	         description: File owner UID
//	         schema:
//	           type: integer
//	       X-LXD-gid:
//	         description: File owner GID
//	         schema:
//	           type: integer
//	       X-LXD-mode:
//	         description: Mode mask
//	         schema:
//	           type: integer
//	       X-LXD-modified:
//	         description: Last modified date
//	         schema:
//	           type: string
//	       X-LXD-type:
//	         description: Type of file (file, symlink or directory)
//	         schema:
//	           type: string
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "404":
//	    $ref: "#/responses/NotFound"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func instanceFileHead(inst instance.Instance, path string) response.Response {
	revert := revert.New()
	defer revert.Fail()

	// Get a SFTP client.
	client, err := inst.FileSFTP()
	if err != nil {
		return response.InternalError(err)
	}

	revert.Add(func() { _ = client.Close() })

	// Get the file stats.
	stat, err := client.Lstat(path)
	if err != nil {
		return response.SmartError(err)
	}

	fileType := "file"
	if stat.Mode().IsDir() {
		fileType = "directory"
	} else if stat.Mode()&os.ModeSymlink == os.ModeSymlink {
		fileType = "symlink"
	}

	fs, _ := stat.Sys().(*sftp.FileStat)

	// Prepare the response.
	headers := map[string]string{
		"X-LXD-uid":      fmt.Sprint(fs.UID),
		"X-LXD-gid":      fmt.Sprint(fs.GID),
		"X-LXD-mode":     fmt.Sprintf("%04o", stat.Mode().Perm()),
		"X-LXD-modified": stat.ModTime().UTC().String(),
		"X-LXD-type":     fileType,
	}

	if fileType == "file" {
		headers["Content-Type"] = "application/octet-stream"
		headers["Content-Length"] = fmt.Sprint(stat.Size())
	}

	// Return an empty body (per RFC for HEAD).
	return response.ManualResponse(func(w http.ResponseWriter) error {
		// Set the headers.
		for k, v := range headers {
			w.Header().Set(k, v)
		}

		// Flush the connection.
		w.WriteHeader(http.StatusOK)
		return nil
	})
}

// For containers we can only run chown/chgrp if target uid/gid is within uidmap allowed range.
func applyEffectiveFileOwnership(inst instance.Instance, headers *shared.LXDFileHeaders, file *sftp.File) error {
	uid := headers.UID
	gid := headers.GID
	if inst.Type() != instancetype.Container {
		return nil
	}

	c, ok := inst.(instance.Container)
	if !ok {
		return fmt.Errorf("Invalid instance type: %T", inst)
	}

	idmapset, err := c.CurrentIdmap()
	if err != nil {
		return err
	}

	if idmapset == nil {
		return nil
	}

	idmapranges, err := idmapset.ValidRanges()
	if len(idmapranges) != 2 {
		return err
	}

	l := logger.AddContext(logger.Ctx{"project": inst.Project().Name, "instance": inst.Name(), "file": file.Name()})
	for _, idmaprange := range idmapranges {
		if idmaprange.Isuid && !idmaprange.Contains(headers.UID) {
			l.Info("Requested UID not within idmap range", logger.Ctx{"uid": uid})
			uid = -1
		}

		if idmaprange.Isgid && !idmaprange.Contains(headers.GID) {
			l.Info("Requested GID not within idmap range", logger.Ctx{"gid": gid})
			gid = -1
		}
	}

	// -1 leaves the id unchanged
	err = file.Chown(int(uid), int(gid))
	if err != nil {
		return err
	}

	return nil
}

// swagger:operation POST /1.0/instances/{name}/files instances instance_files_post
//
//	Create or replace a file
//
//	Creates a new file in the instance.
//
//	---
//	consumes:
//	  - application/octet-stream
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: path
//	    description: Path to the file
//	    type: string
//	    example: default
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	  - in: body
//	    name: raw_file
//	    description: Raw file content
//	  - in: header
//	    name: X-LXD-uid
//	    description: Base 10 32-bit integer for the file owner UID
//	    schema:
//	      type: integer
//	    example: 1000
//	  - in: header
//	    name: X-LXD-gid
//	    description: Base 10 32-bit integer for the file owner GID
//	    schema:
//	      type: integer
//	    example: 1000
//	  - in: header
//	    name: X-LXD-mode
//	    description: Base 10 (no leading `0`) or base 8 (leading `0`) unix permissions bits (other bits are truncated)
//	    schema:
//	      type: integer
//	    example: 0644
//	  - in: header
//	    name: X-LXD-modify-perm
//	    description: Comma-separated list of permissions to set for pre-existing files (0 or more of `uid`, `gid`, `mode`)
//	    schema:
//	      type: integer
//	    example: uid,gid,mode
//	  - in: header
//	    name: X-LXD-type
//	    description: Type of file (file, symlink or directory)
//	    schema:
//	      type: string
//	    example: file
//	  - in: header
//	    name: X-LXD-write
//	    description: Write mode (overwrite or append)
//	    schema:
//	      type: string
//	    example: overwrite
//	responses:
//	  "200":
//	    $ref: "#/responses/EmptySyncResponse"
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "404":
//	    $ref: "#/responses/NotFound"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func instanceFilePost(s *state.State, inst instance.Instance, path string, r *http.Request) response.Response {
	// Get a SFTP client.
	client, err := inst.FileSFTP()
	if err != nil {
		return response.InternalError(err)
	}

	defer func() { _ = client.Close() }()

	// Extract file ownership and mode from headers
	headers, err := shared.ParseLXDFileHeaders(r.Header)
	if err != nil {
		return response.BadRequest(err)
	}

	// Check if the file already exists.
	_, err = client.Stat(path)
	exists := err == nil

	switch headers.Type {
	case "file":
		fileMode := os.O_RDWR

		if headers.Write == "overwrite" {
			fileMode |= os.O_CREATE | os.O_TRUNC
		}

		// Open/create the file.
		file, err := client.OpenFile(path, fileMode)
		if err != nil {
			return response.SmartError(err)
		}

		defer func() { _ = file.Close() }()

		// Go to the end of the file.
		_, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			return response.InternalError(err)
		}

		// Transfer the file into the instance.
		_, err = io.Copy(file, r.Body)
		if err != nil {
			return response.InternalError(err)
		}

		/* backwards-compat: the permissions headers did not modify permissions
		 * for existing files before the `instances_files_modify_permissions`
		 * api extension.
		 */
		if !exists || headers.ModeModifyExisting {
			if headers.Mode >= 0 {
				err = file.Chmod(fs.FileMode(headers.Mode))
				if err != nil {
					return response.SmartError(err)
				}
			}
		}

		// Set file ownership.
		if !exists || headers.UIDModifyExisting || headers.GIDModifyExisting {
			if headers.UID >= 0 || headers.GID >= 0 {
				err = applyEffectiveFileOwnership(inst, headers, file)
				if err != nil {
					return response.SmartError(err)
				}
			}
		}

		s.Events.SendLifecycle(inst.Project().Name, lifecycle.InstanceFilePushed.Event(inst, logger.Ctx{"path": path}))
		return response.EmptySyncResponse
	case "symlink":
		// Figure out target.
		target, err := io.ReadAll(r.Body)
		if err != nil {
			return response.InternalError(err)
		}

		// Check if already setup.
		currentTarget, err := client.ReadLink(path)
		if err == nil && currentTarget == string(target) {
			return response.EmptySyncResponse
		}

		// Create the symlink.
		err = client.Symlink(string(target), path)
		if err != nil {
			return response.SmartError(err)
		}

		s.Events.SendLifecycle(inst.Project().Name, lifecycle.InstanceFilePushed.Event(inst, logger.Ctx{"path": path}))
		return response.EmptySyncResponse
	case "directory":
		// Check if it already exists.
		if exists {
			return response.EmptySyncResponse
		}

		// Create the directory.
		err = client.Mkdir(path)
		if err != nil {
			return response.SmartError(err)
		}

		// Set file permissions.
		if headers.Mode < 0 {
			// Default mode for directories (sftp doesn't know about umask).
			headers.Mode = 0750
		}

		err = client.Chmod(path, fs.FileMode(headers.Mode))
		if err != nil {
			return response.SmartError(err)
		}

		// Set file ownership.
		if headers.UID >= 0 || headers.GID >= 0 {
			err = client.Chown(path, int(headers.UID), int(headers.GID))
			if err != nil {
				return response.SmartError(err)
			}
		}

		s.Events.SendLifecycle(inst.Project().Name, lifecycle.InstanceFilePushed.Event(inst, logger.Ctx{"path": path}))
		return response.EmptySyncResponse
	}

	return response.BadRequest(fmt.Errorf("Bad file type: %s", headers.Type))
}

// swagger:operation DELETE /1.0/instances/{name}/files instances instance_files_delete
//
//	Delete a file
//
//	Removes the file.
//
//	---
//	produces:
//	  - application/json
//	parameters:
//	  - in: query
//	    name: path
//	    description: Path to the file
//	    type: string
//	    example: default
//	  - in: query
//	    name: project
//	    description: Project name
//	    type: string
//	    example: default
//	responses:
//	  "200":
//	    $ref: "#/responses/EmptySyncResponse"
//	  "400":
//	    $ref: "#/responses/BadRequest"
//	  "403":
//	    $ref: "#/responses/Forbidden"
//	  "404":
//	    $ref: "#/responses/NotFound"
//	  "500":
//	    $ref: "#/responses/InternalServerError"
func instanceFileDelete(s *state.State, inst instance.Instance, path string) response.Response {
	// Get a SFTP client.
	client, err := inst.FileSFTP()
	if err != nil {
		return response.InternalError(err)
	}

	defer func() { _ = client.Close() }()

	// Delete the file.
	err = client.Remove(path)
	if err != nil {
		return response.SmartError(err)
	}

	s.Events.SendLifecycle(inst.Project().Name, lifecycle.InstanceFileDeleted.Event(inst, logger.Ctx{"path": path}))
	return response.EmptySyncResponse
}
