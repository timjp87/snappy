// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2015-2016 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/ubuntu-core/snappy/asserts"
	"github.com/ubuntu-core/snappy/dirs"
	"github.com/ubuntu-core/snappy/i18n"
	"github.com/ubuntu-core/snappy/interfaces"
	"github.com/ubuntu-core/snappy/overlord/auth"
	"github.com/ubuntu-core/snappy/overlord/ifacestate"
	"github.com/ubuntu-core/snappy/overlord/snapstate"
	"github.com/ubuntu-core/snappy/overlord/state"
	"github.com/ubuntu-core/snappy/progress"
	"github.com/ubuntu-core/snappy/release"
	"github.com/ubuntu-core/snappy/snap"
	"github.com/ubuntu-core/snappy/snappy"
	"github.com/ubuntu-core/snappy/store"
)

var api = []*Command{
	rootCmd,
	sysInfoCmd,
	loginCmd,
	logoutCmd,
	appIconCmd,
	findCmd,
	snapsCmd,
	snapCmd,
	//FIXME: renenable config for GA
	//snapConfigCmd,
	interfacesCmd,
	assertsCmd,
	assertsFindManyCmd,
	eventsCmd,
	stateChangeCmd,
	stateChangesCmd,
}

var (
	rootCmd = &Command{
		Path:    "/",
		GuestOK: true,
		GET:     SyncResponse([]string{"TBD"}, nil).Self,
	}

	sysInfoCmd = &Command{
		Path:    "/v2/system-info",
		GuestOK: true,
		GET:     sysInfo,
	}

	loginCmd = &Command{
		Path:     "/v2/login",
		POST:     loginUser,
		SudoerOK: true,
	}

	logoutCmd = &Command{
		Path:     "/v2/logout",
		POST:     logoutUser,
		SudoerOK: true,
	}

	appIconCmd = &Command{
		Path:   "/v2/icons/{name}/icon",
		UserOK: true,
		GET:    appIconGet,
	}

	findCmd = &Command{
		Path:   "/v2/find",
		UserOK: true,
		GET:    searchStore,
	}

	snapsCmd = &Command{
		Path:   "/v2/snaps",
		UserOK: true,
		GET:    getSnapsInfo,
		POST:   sideloadSnap,
	}

	snapCmd = &Command{
		Path:   "/v2/snaps/{name}",
		UserOK: true,
		GET:    getSnapInfo,
		POST:   postSnap,
	}
	//FIXME: renenable config for GA
	/*
		snapConfigCmd = &Command{
			Path: "/v2/snaps/{name}/config",
			GET:  snapConfig,
			PUT:  snapConfig,
		}
	*/

	interfacesCmd = &Command{
		Path:   "/v2/interfaces",
		UserOK: true,
		GET:    getInterfaces,
		POST:   changeInterfaces,
	}

	// TODO: allow to post assertions for UserOK? they are verified anyway
	assertsCmd = &Command{
		Path: "/v2/assertions",
		POST: doAssert,
	}

	assertsFindManyCmd = &Command{
		Path:   "/v2/assertions/{assertType}",
		UserOK: true,
		GET:    assertsFindMany,
	}

	eventsCmd = &Command{
		Path: "/v2/events",
		GET:  getEvents,
	}

	stateChangeCmd = &Command{
		Path:   "/v2/changes/{id}",
		UserOK: true,
		GET:    getChange,
		POST:   abortChange,
	}

	stateChangesCmd = &Command{
		Path:   "/v2/changes",
		UserOK: true,
		GET:    getChanges,
	}
)

func sysInfo(c *Command, r *http.Request) Response {
	m := map[string]string{
		"series": release.Series,
	}

	return SyncResponse(m, nil)
}

type loginResponseData struct {
	Macaroon   string   `json:"macaroon,omitempty"`
	Discharges []string `json:"discharges,omitempty"`
}

func loginUser(c *Command, r *http.Request) Response {
	var loginData struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Otp      string `json:"otp"`
	}

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&loginData); err != nil {
		return BadRequest("cannot decode login data from request body: %v", err)
	}

	macaroon, err := store.RequestPackageAccessMacaroon()
	if err != nil {
		return InternalError(err.Error())
	}

	discharge, err := store.DischargeAuthCaveat(loginData.Username, loginData.Password, macaroon, loginData.Otp)
	switch err {
	case store.ErrAuthenticationNeeds2fa:
		return SyncResponse(&resp{
			Type: ResponseTypeError,
			Result: &errorResult{
				Kind:    errorKindTwoFactorRequired,
				Message: err.Error(),
			},
			Status: http.StatusUnauthorized,
		}, nil)
	case store.Err2faFailed:
		return SyncResponse(&resp{
			Type: ResponseTypeError,
			Result: &errorResult{
				Kind:    errorKindTwoFactorFailed,
				Message: err.Error(),
			},
			Status: http.StatusUnauthorized,
		}, nil)
	default:
		return Unauthorized(err.Error())
	case nil:
		// continue
	}

	overlord := c.d.overlord
	state := overlord.State()
	state.Lock()
	_, err = auth.NewUser(state, loginData.Username, macaroon, []string{discharge})
	state.Unlock()
	if err != nil {
		return InternalError("cannot persist authentication details: %v", err)
	}

	result := loginResponseData{
		Macaroon:   macaroon,
		Discharges: []string{discharge},
	}
	return SyncResponse(result, nil)
}

func logoutUser(c *Command, r *http.Request) Response {
	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	user, err := UserFromRequest(state, r)
	if err != nil {
		return BadRequest("not logged in")
	}
	err = auth.RemoveUser(state, user.ID)
	if err != nil {
		return InternalError(err.Error())
	}

	return SyncResponse(nil, nil)
}

// UserFromRequest extracts user information from request and return the respective user in state, if valid
// It requires the state to be locked
func UserFromRequest(st *state.State, req *http.Request) (*auth.UserState, error) {
	// extract macaroons data from request
	header := req.Header.Get("Authorization")
	if header == "" {
		return nil, auth.ErrInvalidAuth
	}

	authorizationData := strings.SplitN(header, " ", 2)
	if len(authorizationData) != 2 || authorizationData[0] != "Macaroon" {
		return nil, fmt.Errorf("authorization header misses Macaroon prefix")
	}

	var macaroon string
	var discharges []string
	for _, field := range strings.Split(authorizationData[1], ",") {
		field := strings.TrimSpace(field)
		if strings.HasPrefix(field, `root="`) {
			macaroon = strings.TrimSuffix(field[6:], `"`)
		}
		if strings.HasPrefix(field, `discharge="`) {
			discharges = append(discharges, strings.TrimSuffix(field[11:], `"`))
		}
	}

	if macaroon == "" || len(discharges) == 0 {
		return nil, fmt.Errorf("invalid authorization header")
	}

	user, err := auth.CheckMacaroon(st, macaroon, discharges)
	return user, err
}

type metarepo interface {
	Snap(string, string, store.Authenticator) (*snap.Info, error)
	FindSnaps(string, string, store.Authenticator) ([]*snap.Info, error)
	SuggestedCurrency() string
}

var newRemoteRepo = func() metarepo {
	return snappy.NewConfiguredUbuntuStoreSnapRepository()
}

var muxVars = mux.Vars

func getSnapInfo(c *Command, r *http.Request) Response {
	vars := muxVars(r)
	name := vars["name"]

	channel := ""
	remoteRepo := newRemoteRepo()
	suggestedCurrency := remoteRepo.SuggestedCurrency()

	localSnap, active, err := localSnapInfo(c.d.overlord.State(), name)
	if err != nil {
		return InternalError("%v", err)
	}

	if localSnap != nil {
		channel = localSnap.Channel
	}

	auther, err := c.d.auther(r)
	if err != nil && err != auth.ErrInvalidAuth {
		return InternalError("%v", err)
	}

	remoteSnap, _ := remoteRepo.Snap(name, channel, auther)

	if localSnap == nil && remoteSnap == nil {
		return NotFound("cannot find snap %q", name)
	}

	route := c.d.router.Get(c.Path)
	if route == nil {
		return InternalError("router can't find route for snap %s", name)
	}

	url, err := route.URL("name", name)
	if err != nil {
		return InternalError("route can't build URL for snap %s: %v", name, err)
	}

	result := webify(mapSnap(localSnap, active, remoteSnap), url.String())

	meta := &Meta{
		SuggestedCurrency: suggestedCurrency,
	}
	return SyncResponse(result, meta)
}

func webify(result map[string]interface{}, resource string) map[string]interface{} {
	result["resource"] = resource

	icon, ok := result["icon"].(string)
	if !ok || icon == "" || strings.HasPrefix(icon, "http") {
		return result
	}
	result["icon"] = ""

	route := appIconCmd.d.router.Get(appIconCmd.Path)
	if route != nil {
		name, _ := result["name"].(string)
		url, err := route.URL("name", name)
		if err == nil {
			result["icon"] = url.String()
		}
	}

	return result
}

func searchStore(c *Command, r *http.Request) Response {
	route := c.d.router.Get(snapCmd.Path)
	if route == nil {
		return InternalError("router can't find route for snaps")
	}

	query := r.URL.Query()

	auther, err := c.d.auther(r)
	if err != nil && err != auth.ErrInvalidAuth {
		return InternalError("%v", err)
	}

	remoteRepo := newRemoteRepo()
	found, err := remoteRepo.FindSnaps(query.Get("q"), query.Get("channel"), auther)
	if err != nil {
		return InternalError("%v", err)
	}

	meta := &Meta{
		SuggestedCurrency: remoteRepo.SuggestedCurrency(),
	}

	results := make([]*json.RawMessage, len(found))
	for i, x := range found {
		resource := ""
		url, err := route.URL("name", x.Name())
		if err == nil {
			resource = url.String()
		}

		data, err := json.Marshal(webify(mapSnap(nil, false, x), resource))
		if err != nil {
			return InternalError("%v", err)
		}
		raw := json.RawMessage(data)
		results[i] = &raw
	}

	return SyncResponse(results, meta)
}

// plural!
func getSnapsInfo(c *Command, r *http.Request) Response {
	route := c.d.router.Get(snapCmd.Path)
	if route == nil {
		return InternalError("router can't find route for snaps")
	}

	sources := make([]string, 0, 2)
	query := r.URL.Query()

	var includeStore, includeLocal bool
	if len(query["sources"]) > 0 {
		// XXX use query.Get to make this easier to follow
		for _, v := range strings.Split(query["sources"][0], ",") {
			if v == "store" {
				includeStore = true
			} else if v == "local" {
				includeLocal = true
			}
		}
	} else {
		includeStore = true
		includeLocal = true
	}

	searchTerm := query.Get("q")

	var includeTypes []string
	if len(query["types"]) > 0 {
		includeTypes = strings.Split(query["types"][0], ",")
	}

	var aboutSnaps []aboutSnap
	var remoteSnapMap map[string]*snap.Info

	if includeLocal {
		sources = append(sources, "local")
		aboutSnaps, _ = allLocalSnapInfos(c.d.overlord.State())
	}

	var suggestedCurrency string

	if includeStore {
		remoteSnapMap = make(map[string]*snap.Info)

		remoteRepo := newRemoteRepo()

		auther, err := c.d.auther(r)
		if err != nil && err != auth.ErrInvalidAuth {
			return InternalError("%v", err)
		}

		// repo.Find("") finds all
		//
		// TODO: Instead of ignoring the error from Find:
		//   * if there are no results, return an error response.
		//   * If there are results at all (perhaps local), include a
		//     warning in the response
		found, _ := remoteRepo.FindSnaps(searchTerm, "", auther)
		suggestedCurrency = remoteRepo.SuggestedCurrency()

		sources = append(sources, "store")

		for _, snap := range found {
			remoteSnapMap[snap.Name()] = snap
		}
	}

	seen := make(map[string]bool)
	results := make([]*json.RawMessage, 0, len(aboutSnaps)+len(remoteSnapMap))

	addResult := func(name string, m map[string]interface{}) {
		if seen[name] {
			return
		}
		seen[name] = true

		// TODO Search the store for "content" with multiple values. See:
		//      https://wiki.ubuntu.com/AppStore/Interfaces/ClickPackageIndex#Search
		if len(includeTypes) > 0 && !resultHasType(m, includeTypes) {
			return
		}

		resource := ""
		url, err := route.URL("name", name)
		if err == nil {
			resource = url.String()
		}

		data, err := json.Marshal(webify(m, resource))
		if err != nil {
			return
		}
		raw := json.RawMessage(data)
		results = append(results, &raw)
	}

	for _, about := range aboutSnaps {
		info := about.info
		name := info.Name()
		// strings.Contains(name, "") is true
		if strings.Contains(name, searchTerm) {
			active := about.snapst.Active
			addResult(name, mapSnap(info, active, remoteSnapMap[name]))
		}
	}

	for name, remoteSnap := range remoteSnapMap {
		addResult(name, mapSnap(nil, false, remoteSnap))
	}

	meta := &Meta{
		Sources: sources,
		Paging: &Paging{
			Page:  1,
			Pages: 1,
		},
		SuggestedCurrency: suggestedCurrency,
	}
	return SyncResponse(results, meta)
}

func resultHasType(r map[string]interface{}, allowedTypes []string) bool {
	for _, t := range allowedTypes {
		if r["type"] == t {
			return true
		}
	}
	return false
}

// licenseData holds details about the snap license, and may be
// marshaled back as an error when the license agreement is pending,
// and is expected as input to accept (or not) that license
// agreement. As such, its field names are part of the API.
type licenseData struct {
	Intro   string `json:"intro"`
	License string `json:"license"`
	Agreed  bool   `json:"agreed"`
}

func (*licenseData) Error() string {
	return "license agreement required"
}

type snapInstruction struct {
	progress.NullProgress
	Action   string       `json:"action"`
	Channel  string       `json:"channel"`
	DevMode  bool         `json:"devmode"`
	LeaveOld bool         `json:"leave-old"`
	License  *licenseData `json:"license"`

	// The fields below should not be unmarshalled into. Do not export them.
	snap   string
	userID int
}

// Agreed is part of the progress.Meter interface (q.v.)
// ask the user whether they agree to the given license's text
func (inst *snapInstruction) Agreed(intro, license string) bool {
	if inst.License == nil || !inst.License.Agreed || inst.License.Intro != intro || inst.License.License != license {
		inst.License = &licenseData{Intro: intro, License: license, Agreed: false}
		return false
	}

	return true
}

var snapstateInstall = snapstate.Install
var snapstateUpdate = snapstate.Update
var snapstateInstallPath = snapstate.InstallPath
var snapstateGet = snapstate.Get

var errNothingToInstall = errors.New("nothing to install")

func ensureUbuntuCore(st *state.State, targetSnap string, userID int) (*state.TaskSet, error) {
	ubuntuCore := "ubuntu-core"

	if targetSnap == ubuntuCore {
		return nil, errNothingToInstall
	}

	var ss snapstate.SnapState

	err := snapstateGet(st, ubuntuCore, &ss)
	if err != state.ErrNoState {
		return nil, err
	}

	return snapstateInstall(st, ubuntuCore, "stable", userID, 0)
}

func withEnsureUbuntuCore(st *state.State, targetSnap string, userID int, install func() (*state.TaskSet, error)) ([]*state.TaskSet, error) {
	ubuCoreTs, err := ensureUbuntuCore(st, targetSnap, userID)
	if err != nil && err != errNothingToInstall {
		return nil, err
	}

	ts, err := install()
	if err != nil {
		return nil, err
	}

	// ensure main install waits on ubuntu core install
	if ubuCoreTs != nil {
		ts.WaitAll(ubuCoreTs)
		return []*state.TaskSet{ubuCoreTs, ts}, nil
	}

	return []*state.TaskSet{ts}, nil
}

func snapInstall(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	flags := snappy.DoInstallGC
	if inst.LeaveOld {
		flags = 0
	}
	if inst.DevMode {
		flags |= snappy.DeveloperMode
	}

	tsets, err := withEnsureUbuntuCore(st, inst.snap, inst.userID,
		func() (*state.TaskSet, error) {
			return snapstateInstall(st, inst.snap, inst.Channel, inst.userID, flags)
		},
	)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Install %q snap"), inst.snap)
	if inst.Channel != "stable" && inst.Channel != "" {
		msg = fmt.Sprintf(i18n.G("Install %q snap from %q channel"), inst.snap, inst.Channel)
	}
	return msg, tsets, nil

	// FIXME: handle license agreement need to happen in the above
	//        code
	/*
		_, err := snappyInstall(inst.pkg, inst.Channel, flags, inst)
		if err != nil {
			if inst.License != nil && snappy.IsLicenseNotAccepted(err) {
				return inst.License
			}
			return err
		}
	*/
}

func snapUpdate(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	flags := snappy.DoInstallGC
	if inst.LeaveOld {
		flags = 0
	}

	ts, err := snapstateUpdate(st, inst.snap, inst.Channel, inst.userID, flags)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Refresh %q snap"), inst.snap)
	if inst.Channel != "stable" && inst.Channel != "" {
		msg = fmt.Sprintf(i18n.G("Refresh %q snap from %q channel"), inst.snap, inst.Channel)
	}

	return msg, []*state.TaskSet{ts}, nil
}

func snapRemove(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	flags := snappy.DoRemoveGC
	if inst.LeaveOld {
		flags = 0
	}
	ts, err := snapstate.Remove(st, inst.snap, flags)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Remove %q snap"), inst.snap)
	return msg, []*state.TaskSet{ts}, nil
}

func snapRollback(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	// use previous version
	ver := ""
	ts, err := snapstate.Rollback(st, inst.snap, ver)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Rollback %q snap"), inst.snap)
	return msg, []*state.TaskSet{ts}, nil
}

func snapActivate(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	ts, err := snapstate.Activate(st, inst.snap)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Activate %q snap"), inst.snap)
	return msg, []*state.TaskSet{ts}, nil
}

func snapDeactivate(inst *snapInstruction, st *state.State) (string, []*state.TaskSet, error) {
	ts, err := snapstate.Deactivate(st, inst.snap)
	if err != nil {
		return "", nil, err
	}

	msg := fmt.Sprintf(i18n.G("Deactivate %q snap"), inst.snap)
	return msg, []*state.TaskSet{ts}, nil
}

type snapActionFunc func(*snapInstruction, *state.State) (string, []*state.TaskSet, error)

var snapInstructionDispTable = map[string]snapActionFunc{
	"install":    snapInstall,
	"refresh":    snapUpdate,
	"remove":     snapRemove,
	"rollback":   snapRollback,
	"activate":   snapActivate,
	"deactivate": snapDeactivate,
}

func (inst *snapInstruction) dispatch() snapActionFunc {
	return snapInstructionDispTable[inst.Action]
}

func postSnap(c *Command, r *http.Request) Response {
	route := c.d.router.Get(stateChangeCmd.Path)
	if route == nil {
		return InternalError("router can't find route for change")
	}

	decoder := json.NewDecoder(r.Body)
	var inst snapInstruction
	if err := decoder.Decode(&inst); err != nil {
		return BadRequest("can't decode request body into snap instruction: %v", err)
	}

	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	user, err := UserFromRequest(state, r)
	if err == nil {
		inst.userID = user.ID
	} else if err != auth.ErrInvalidAuth {
		return InternalError("%v", err)
	}

	vars := muxVars(r)
	inst.snap = vars["name"]

	impl := inst.dispatch()
	if impl == nil {
		return BadRequest("unknown action %s", inst.Action)
	}

	msg, tsets, err := impl(&inst, state)
	if err != nil {
		return InternalError("cannot %s %q: %v", inst.Action, inst.snap, err)
	}

	chg := newChange(state, inst.Action+"-snap", msg, tsets)
	state.EnsureBefore(0)

	return AsyncResponse(nil, &Meta{Change: chg.ID()})
}

func newChange(st *state.State, kind, summary string, tsets []*state.TaskSet) *state.Change {
	chg := st.NewChange(kind, summary)
	for _, ts := range tsets {
		chg.AddAll(ts)
	}
	return chg
}

const maxReadBuflen = 1024 * 1024

func sideloadSnap(c *Command, r *http.Request) Response {
	route := c.d.router.Get(stateChangeCmd.Path)
	if route == nil {
		return InternalError("cannot find route for change")
	}

	contentType := r.Header.Get("Content-Type")

	if !strings.HasPrefix(contentType, "multipart/") {
		return BadRequest("unknown content type: %s", contentType)
	}

	// POSTs to sideload snaps must be a multipart/form-data file upload.
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return BadRequest("cannot parse POST body: %v", err)
	}

	form, err := multipart.NewReader(r.Body, params["boundary"]).ReadForm(maxReadBuflen)
	if err != nil {
		return BadRequest("cannot read POST form: %v", err)
	}

	var flags snappy.InstallFlags

	if len(form.Value["devmode"]) > 0 && form.Value["devmode"][0] == "true" {
		flags |= snappy.DeveloperMode
	}

	// find the file for the "snap" form field
	var snapBody multipart.File
	var origPath string
out:
	for name, fheaders := range form.File {
		if name != "snap" {
			continue
		}
		for _, fheader := range fheaders {
			snapBody, err = fheader.Open()
			origPath = fheader.Filename
			if err != nil {
				return BadRequest(`cannot open uploaded "snap" file: %v`, err)
			}
			defer snapBody.Close()

			break out
		}
	}
	defer form.RemoveAll()

	if snapBody == nil {
		return BadRequest(`cannot find "snap" file field in provided multipart/form-data payload`)
	}

	tmpf, err := ioutil.TempFile("", "snapd-sideload-pkg-")
	if err != nil {
		return InternalError("cannot create temporary file: %v", err)
	}

	if _, err := io.Copy(tmpf, snapBody); err != nil {
		os.Remove(tmpf.Name())
		return InternalError("cannot copy request into temporary file: %v", err)
	}
	tmpf.Sync()

	tempPath := tmpf.Name()

	if len(form.Value["snap-path"]) > 0 {
		origPath = form.Value["snap-path"][0]
	}

	info, err := readSnapInfo(tempPath)
	if err != nil {
		return InternalError("cannot read snap file: %v", err)
	}
	snapName := info.Name()

	st := c.d.overlord.State()
	st.Lock()
	defer st.Unlock()

	msg := fmt.Sprintf(i18n.G("Install %q snap from file"), snapName)
	if origPath != "" {
		msg = fmt.Sprintf(i18n.G("Install %q snap from file %q"), snapName, origPath)
	}

	var userID int
	user, err := UserFromRequest(st, r)
	if err == nil {
		userID = user.ID
	} else if err != auth.ErrInvalidAuth {
		return InternalError("%v", err)
	}

	tsets, err := withEnsureUbuntuCore(st, snapName, userID,
		func() (*state.TaskSet, error) {
			return snapstateInstallPath(st, snapName, tempPath, "", flags)
		},
	)
	if err != nil {
		return InternalError("cannot install snap file: %v", err)
	}

	chg := newChange(st, "install-snap", msg, tsets)
	chg.Set("api-data", map[string]string{"snap-name": snapName})

	go func() {
		// XXX this needs to be a task in the manager; this is a hack to keep this branch smaller
		<-chg.Ready()
		os.Remove(tempPath)
	}()

	st.EnsureBefore(0)

	return AsyncResponse(nil, &Meta{Change: chg.ID()})
}

func readSnapInfoImpl(snapPath string) (*snap.Info, error) {
	// TODO Only open if in devmode or we have the assertion proving content right.
	snapf, err := snap.Open(snapPath)
	if err != nil {
		return nil, err
	}
	return snap.ReadInfoFromSnapFile(snapf, nil)
}

var readSnapInfo = readSnapInfoImpl

func iconGet(st *state.State, name string) Response {
	info, _, err := localSnapInfo(st, name)
	if err != nil {
		return InternalError("%v", err)
	}
	if info == nil {
		return NotFound("cannot find snap %q", name)
	}

	path := filepath.Clean(snapIcon(info))
	if !strings.HasPrefix(path, dirs.SnapSnapsDir) {
		// XXX: how could this happen?
		return BadRequest("requested icon is not in snap path")
	}

	return FileResponse(path)
}

func appIconGet(c *Command, r *http.Request) Response {
	vars := muxVars(r)
	name := vars["name"]

	return iconGet(c.d.overlord.State(), name)
}

// getInterfaces returns all plugs and slots.
func getInterfaces(c *Command, r *http.Request) Response {
	repo := c.d.overlord.InterfaceManager().Repository()
	return SyncResponse(repo.Interfaces(), nil)
}

// plugJSON aids in marshaling Plug into JSON.
type plugJSON struct {
	Snap        string                 `json:"snap"`
	Name        string                 `json:"plug"`
	Interface   string                 `json:"interface"`
	Attrs       map[string]interface{} `json:"attrs,omitempty"`
	Apps        []string               `json:"apps,omitempty"`
	Label       string                 `json:"label"`
	Connections []interfaces.SlotRef   `json:"connections,omitempty"`
}

// slotJSON aids in marshaling Slot into JSON.
type slotJSON struct {
	Snap        string                 `json:"snap"`
	Name        string                 `json:"slot"`
	Interface   string                 `json:"interface"`
	Attrs       map[string]interface{} `json:"attrs,omitempty"`
	Apps        []string               `json:"apps,omitempty"`
	Label       string                 `json:"label"`
	Connections []interfaces.PlugRef   `json:"connections,omitempty"`
}

// interfaceAction is an action performed on the interface system.
type interfaceAction struct {
	Action string     `json:"action"`
	Plugs  []plugJSON `json:"plugs,omitempty"`
	Slots  []slotJSON `json:"slots,omitempty"`
}

// changeInterfaces controls the interfaces system.
// Plugs can be connected to and disconnected from slots.
// When enableInternalInterfaceActions is true plugs and slots can also be
// explicitly added and removed.
func changeInterfaces(c *Command, r *http.Request) Response {
	var a interfaceAction
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&a); err != nil {
		return BadRequest("cannot decode request body into an interface action: %v", err)
	}
	if a.Action == "" {
		return BadRequest("interface action not specified")
	}
	if !c.d.enableInternalInterfaceActions && a.Action != "connect" && a.Action != "disconnect" {
		return BadRequest("internal interface actions are disabled")
	}
	if len(a.Plugs) > 1 || len(a.Slots) > 1 {
		return NotImplemented("many-to-many operations are not implemented")
	}
	if a.Action != "connect" && a.Action != "disconnect" {
		return BadRequest("unsupported interface action: %q", a.Action)
	}
	if len(a.Plugs) == 0 || len(a.Slots) == 0 {
		return BadRequest("at least one plug and slot is required")
	}

	var summary string
	var taskset *state.TaskSet
	var err error

	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()

	switch a.Action {
	case "connect":
		summary = fmt.Sprintf("Connect %s:%s to %s:%s", a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
		taskset, err = ifacestate.Connect(state, a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
	case "disconnect":
		summary = fmt.Sprintf("Disconnect %s:%s from %s:%s", a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
		taskset, err = ifacestate.Disconnect(state, a.Plugs[0].Snap, a.Plugs[0].Name, a.Slots[0].Snap, a.Slots[0].Name)
	}
	if err != nil {
		return BadRequest("%v", err)
	}

	change := state.NewChange(a.Action+"-snap", summary)
	change.AddAll(taskset)

	state.EnsureBefore(0)

	return AsyncResponse(nil, &Meta{Change: change.ID()})
}

func doAssert(c *Command, r *http.Request) Response {
	b, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return BadRequest("reading assert request body gave %v", err)
	}
	a, err := asserts.Decode(b)
	if err != nil {
		return BadRequest("can't decode request body into an assertion: %v", err)
	}
	// TODO/XXX: turn this into a Change/Task combination
	amgr := c.d.overlord.AssertManager()
	if err := amgr.DB().Add(a); err != nil {
		// TODO: have a specific error to be able to return  409 for not newer revision?
		return BadRequest("assert failed: %v", err)
	}
	// TODO: what more info do we want to return on success?
	return &resp{
		Type:   ResponseTypeSync,
		Status: http.StatusOK,
	}
}

func assertsFindMany(c *Command, r *http.Request) Response {
	assertTypeName := muxVars(r)["assertType"]
	assertType := asserts.Type(assertTypeName)
	if assertType == nil {
		return BadRequest("invalid assert type: %q", assertTypeName)
	}
	headers := map[string]string{}
	q := r.URL.Query()
	for k := range q {
		headers[k] = q.Get(k)
	}
	amgr := c.d.overlord.AssertManager()
	assertions, err := amgr.DB().FindMany(assertType, headers)
	if err == asserts.ErrNotFound {
		return AssertResponse(nil, true)
	} else if err != nil {
		return InternalError("searching assertions failed: %v", err)
	}
	return AssertResponse(assertions, true)
}

func getEvents(c *Command, r *http.Request) Response {
	return EventResponse(c.d.hub)
}

type changeInfo struct {
	ID      string      `json:"id"`
	Kind    string      `json:"kind"`
	Summary string      `json:"summary"`
	Status  string      `json:"status"`
	Tasks   []*taskInfo `json:"tasks,omitempty"`
	Ready   bool        `json:"ready"`
	Err     string      `json:"err,omitempty"`

	SpawnTime time.Time  `json:"spawn-time,omitempty"`
	ReadyTime *time.Time `json:"ready-time,omitempty"`

	Data map[string]*json.RawMessage `json:"data,omitempty"`
}

type taskInfo struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Summary  string           `json:"summary"`
	Status   string           `json:"status"`
	Log      []string         `json:"log,omitempty"`
	Progress taskInfoProgress `json:"progress"`

	SpawnTime time.Time  `json:"spawn-time,omitempty"`
	ReadyTime *time.Time `json:"ready-time,omitempty"`
}

type taskInfoProgress struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

func change2changeInfo(chg *state.Change) *changeInfo {
	status := chg.Status()
	chgInfo := &changeInfo{
		ID:      chg.ID(),
		Kind:    chg.Kind(),
		Summary: chg.Summary(),
		Status:  status.String(),
		Ready:   status.Ready(),

		SpawnTime: chg.SpawnTime(),
	}
	readyTime := chg.ReadyTime()
	if !readyTime.IsZero() {
		chgInfo.ReadyTime = &readyTime
	}
	if err := chg.Err(); err != nil {
		chgInfo.Err = err.Error()
	}

	tasks := chg.Tasks()
	taskInfos := make([]*taskInfo, len(tasks))
	for j, t := range tasks {
		done, total := t.Progress()
		taskInfo := &taskInfo{
			ID:      t.ID(),
			Kind:    t.Kind(),
			Summary: t.Summary(),
			Status:  t.Status().String(),
			Log:     t.Log(),
			Progress: taskInfoProgress{
				Done:  done,
				Total: total,
			},
			SpawnTime: t.SpawnTime(),
		}
		readyTime := t.ReadyTime()
		if !readyTime.IsZero() {
			taskInfo.ReadyTime = &readyTime
		}
		taskInfos[j] = taskInfo
	}
	chgInfo.Tasks = taskInfos

	var data map[string]*json.RawMessage
	if chg.Get("api-data", &data) == nil {
		chgInfo.Data = data
	}

	return chgInfo
}

func getChange(c *Command, r *http.Request) Response {
	chID := muxVars(r)["id"]
	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()
	chg := state.Change(chID)
	if chg == nil {
		return NotFound("cannot find change with id %q", chID)
	}

	return SyncResponse(change2changeInfo(chg), nil)
}

func getChanges(c *Command, r *http.Request) Response {
	query := r.URL.Query()
	qselect := query.Get("select")
	if qselect == "" {
		qselect = "in-progress"
	}
	var filter func(*state.Change) bool
	switch qselect {
	case "all":
		filter = func(*state.Change) bool { return true }
	case "in-progress":
		filter = func(chg *state.Change) bool { return !chg.Status().Ready() }
	case "ready":
		filter = func(chg *state.Change) bool { return chg.Status().Ready() }
	default:
		return BadRequest("select should be one of: all,in-progress,ready")
	}

	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()
	chgs := state.Changes()
	chgInfos := make([]*changeInfo, 0, len(chgs))
	for _, chg := range chgs {
		if !filter(chg) {
			continue
		}
		chgInfos = append(chgInfos, change2changeInfo(chg))
	}
	return SyncResponse(chgInfos, nil)
}

func abortChange(c *Command, r *http.Request) Response {
	chID := muxVars(r)["id"]
	state := c.d.overlord.State()
	state.Lock()
	defer state.Unlock()
	chg := state.Change(chID)
	if chg == nil {
		return NotFound("cannot find change with id %q", chID)
	}

	var reqData struct {
		Action string `json:"action"`
	}

	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&reqData); err != nil {
		return BadRequest("cannot decode data from request body: %v", err)
	}

	if reqData.Action != "abort" {
		return BadRequest("change action %q is unsupported", reqData.Action)
	}

	if chg.Status().Ready() {
		return BadRequest("cannot abort change %s with nothing pending", chID)
	}

	chg.Abort()

	return SyncResponse(change2changeInfo(chg), nil)
}
