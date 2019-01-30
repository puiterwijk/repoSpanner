package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"

	"golang.org/x/net/http2"
	"repospanner.org/repospanner/server/constants"
	"repospanner.org/repospanner/server/datastructures"
	"repospanner.org/repospanner/server/storage"
)

var (
	debug       bool
	keysDeleted bool
	lastcommand string
	ackchan     chan<- string
)

func failIfError(err error, msg string) {
	if err != nil {
		if debug {
			fmt.Println("Error: ", err)
		}
		failNow(msg)
	}
}

func failNow(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	if ackchan != nil {
		acknowledgeCommand()
	}
	panic("Erroring")
}

func getRequest() datastructures.HookRunRequest {
	if !constants.VersionBuiltIn() {
		fmt.Fprintf(os.Stderr, "Invalid build")
		os.Exit(1)
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("repoSpanner Hook Runner " + constants.PublicVersionString())
		os.Exit(0)
	}

	breq, err := ioutil.ReadAll(os.Stdin)
	failIfError(err, "Error reading request")

	var request datastructures.HookRunRequest
	err = json.Unmarshal(breq, &request)
	failIfError(err, "Error parsing request")

	debug = request.Debug
	return request
}

func cloneRepository(request datastructures.HookRunRequest) string {
	workdir, err := ioutil.TempDir("", "repospanner_hook_runner_")
	failIfError(err, "Error creating runner work directory")
	defer os.RemoveAll(workdir)
	err = os.Mkdir(path.Join(workdir, "hookrun"), 0755)
	failIfError(err, "Error creating runner work directory")
	err = os.Mkdir(path.Join(workdir, "keys"), 0700)
	failIfError(err, "Error creating runner keys directory")
	err = ioutil.WriteFile(
		path.Join(workdir, "keys", "ca.crt"),
		[]byte(request.ClientCaCert),
		0600,
	)
	failIfError(err, "Error writing keys")
	err = ioutil.WriteFile(
		path.Join(workdir, "keys", "client.crt"),
		[]byte(request.ClientCert),
		0600,
	)
	failIfError(err, "Error writing keys")
	err = ioutil.WriteFile(
		path.Join(workdir, "keys", "client.key"),
		[]byte(request.ClientKey),
		0600,
	)
	failIfError(err, "Error writing keys")

	cmd := exec.Command(
		"git",
		"clone",
		"--bare",
		"--config", "http.sslcainfo="+path.Join(workdir, "keys", "ca.crt"),
		"--config", "http.sslcert="+path.Join(workdir, "keys", "client.crt"),
		"--config", "http.sslkey="+path.Join(workdir, "keys", "client.key"),
		request.RPCURL+"/rpc/repo/"+request.ProjectName+".git",
		path.Join(workdir, "hookrun", "clone"),
	)
	if debug {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err = cmd.Run()
	failIfError(err, "Error cloning the repository")

	// Delete Git's .sample hook files
	hookfiles, err := ioutil.ReadDir(path.Join(workdir, "hookrun", "clone", "hooks"))
	failIfError(err, "Error getting hookfiles")
	for _, hookfile := range hookfiles {
		err := os.Remove(path.Join(workdir, "hookrun", "clone", "hooks", hookfile.Name()))
		failIfError(err, "Error removing hookfile")
	}

	return workdir
}

func fetchFakeRefs(request datastructures.HookRunRequest, workdir string) {
	// At this point, we have started preparations.
	tos := make([]string, 0)

	for refname, req := range request.Requests {
		if req[1] == string(storage.ZeroID) {
			// This is a deletion. Nothing to fetch for that
			continue
		}
		tos = append(tos, fmt.Sprintf("refs/heads/fake/%s/%s", request.PushUUID, refname))
	}

	if len(tos) == 0 {
		return
	}

	cmdstr := []string{
		"fetch",
		"origin",
	}
	cmdstr = append(cmdstr, tos...)
	cmd := exec.Command(
		"git",
		cmdstr...,
	)
	cmd.Dir = path.Join(workdir, "hookrun", "clone")
	if debug {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	err := cmd.Run()
	failIfError(err, "Error grabbing the To objects")
}

func deleteKeys(request *datastructures.HookRunRequest, workdir string) {
	// We are done cloning, let's remove the keys from file system and memory
	// This is under the phrasing "What's not there, can't be stolen"
	err := os.RemoveAll(path.Join(workdir, "keys"))
	failIfError(err, "Error removing keys")
	request.ClientCert = ""
	request.ClientKey = ""
	request.ClientCaCert = ""

	keysDeleted = true
}

func assertKeysDeleted() {
	if !keysDeleted {
		failNow("Keys not deleted prior to hook run")
	}
}

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "Error occured")
			os.Exit(1)
		}
	}()

	request := getRequest()

	if debug {
		fmt.Println("repoSpanner Hook Runner " + constants.PublicVersionString())
	}

	// Before doing anything else, lower privileges
	if request.User != 0 {
		err := dropToUser(request.User)
		failIfError(err, "Error dropping privileges")
	}

	// At this moment, we have lowered privileges. Clone the repo
	workdir := cloneRepository(request)

	// Get the hook scripts
	getScripts(request, workdir)

	// Get the "control" file.
	control := os.NewFile(3, "control")
	if control == nil {
		failNow("No control file passed?")
		return
	}
	cmdchan := runControlChannel(control)

	// At this moment, we have finished all preparations we can so far.
	// Now wait for commands.
	processCommands(&request, workdir, cmdchan)
}

func runControlChannel(controlfile *os.File) <-chan string {
	cmdchan := make(chan string)
	sendackchan := make(chan string)
	ackchan = sendackchan

	// Goroutine to read commands from controlfile and put to cmdchan
	go func() {
		buffer := make([]byte, 1024)
		read := 0
		for {
			n, err := controlfile.Read(buffer[read:])
			failIfError(err, "Error reading control file")
			read += n

		checkforcommand:
			for i := 0; i < read; i++ {
				// Try to find a newline deliniating a full command
				if buffer[i] == '\n' {
					// Full command received, send
					cmdchan <- string(buffer[0:i])

					// Move everything after back forward for the next run
					copy(buffer[0:], buffer[i+1:read])
					read -= (i + 1)

					continue checkforcommand
				}
			}
		}
	}()

	// Goroutine to read acks from ackchan and put to controlfile
	go func() {
		for ack := range sendackchan {
			n, err := controlfile.WriteString(ack + "\n")
			failIfError(err, "Error writing reply to control file")
			if n != len(ack)+1 {
				failNow("Not full acknowledgment written")
			}
		}
	}()

	return cmdchan
}

func acknowledgeCommand(resp constants.Command) {
	ackchan <- fmt.Sprintf("%s %s\n", resp, lastcommand)
}

func processCommands(request *datastructures.HookRunRequest, workdir string, cmdchan <-chan string) {
	for cmd := range cmdchan {
		if cmd == "" {
			failNow("Empty command received by hook runner?")
		}

		cmdparts := strings.Split(cmd, " ")
		lastcommand := cmdparts[0]
		command := constants.Command(cmdparts[1])
		var args []string
		if len(cmdparts) > 2 {
			args = cmdparts[2:]
		}

		switch command {
		case constants.CmdFetch:
			fetchFakeRefs(*request, workdir)
			deleteKeys(request, workdir)
			acknowledgeCommand(constants.CmdRespOK, chal, ackchan)
			continue

		case constants.CmdRun:
			hookname := args[0]

			if hookname == "update" {
				for branch, req := range request.Requests {
					runHook(*request, workdir, hookname, branch, req)
				}
			} else {
				runHook(*request, workdir, hookname, "", [2]string{"", ""})
			}

			acknowledgeCommand(chal, ackchan)

			continue

		case constants.CmdQuit:
			acknowledgeCommand(chal, ackchan)
			close(ackchan)
			return

		}

		failNow("Invalid command received: " + cmd)
	}
}

func getHookArgs(request datastructures.HookRunRequest, hookname, branch string, req [2]string) ([]string, io.Reader) {
	buf := bytes.NewBuffer(nil)
	if hookname == "update" {
		return []string{branch, req[0], req[1]}, buf
	} else {
		for branch, req := range request.Requests {
			fmt.Fprintf(buf, "%s %s %s\n", req[0], req[1], branch)
		}
		return []string{}, buf
	}
}

func runHook(request datastructures.HookRunRequest, workdir, hookname string, branch string, req [2]string) {
	assertKeysDeleted()

	hookArgs, hookbuf := getHookArgs(request, hookname, branch, req)
	usebwrap, bwrapcmd := getBwrapConfig(request.BwrapConfig)

	var cmd *exec.Cmd
	if usebwrap {
		bwrapcmd = append(
			bwrapcmd,
			"--bind", path.Join(workdir, "hookrun"), "/hookrun",
			"--chdir", "/hookrun/clone",
			"--setenv", "GIT_DIR", "/hookrun/clone",
			"--setenv", "HOOKTYPE", hookname,
		)

		for key, val := range request.ExtraEnv {
			bwrapcmd = append(
				bwrapcmd,
				"--setenv", "extra_"+key, val,
			)
		}

		bwrapcmd = append(
			bwrapcmd,
			path.Join("/hookrun", hookname),
		)
		bwrapcmd = append(
			bwrapcmd,
			hookArgs...,
		)
		cmd = exec.Command(
			"bwrap",
			bwrapcmd...,
		)
	} else {
		cmd = exec.Command(
			path.Join(workdir, "hookrun", hookname),
			hookArgs...,
		)
		cmd.Dir = path.Join(workdir, "hookrun", "clone")
		cmd.Env = []string{
			"GIT_DIR=" + cmd.Dir,
		}

		for key, val := range request.ExtraEnv {
			cmd.Env = append(
				cmd.Env,
				"extra_"+key+"="+val,
			)
		}
	}

	cmd.Stdin = hookbuf
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	err := cmd.Run()
	if err == nil {
		return
	}

	_, iseerr := err.(*exec.ExitError)
	if iseerr {
		fmt.Fprintln(os.Stderr, "Hook returned error")
		panic("Aborting")
	}
	failIfError(err, "Error running hook")
}

func getScripts(request datastructures.HookRunRequest, workdir string) {
	cert, err := tls.LoadX509KeyPair(
		path.Join(workdir, "keys", "client.crt"),
		path.Join(workdir, "keys", "client.key"),
	)
	failIfError(err, "Unable to load client keys")

	certpool := x509.NewCertPool()
	certpool.AppendCertsFromPEM([]byte(request.ClientCaCert))

	clientconf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2"},
		RootCAs:      certpool,
	}
	transport := &http.Transport{
		TLSClientConfig: clientconf,
	}
	err = http2.ConfigureTransport(transport)
	failIfError(err, "Error reconfiguring transport")
	clnt := &http.Client{
		Transport: transport,
	}

	// Grab the hook scripts
	for hook, object := range request.HookObjects {
		script, err := os.Create(path.Join(workdir, "hookrun", hook))
		failIfError(err, "Error opening script file")
		resp, err := clnt.Get(
			request.RPCURL + "/rpc/object/single/admin/hooks.git/" + object,
		)
		failIfError(err, "Error retrieving hook script")
		if resp.StatusCode != 200 {
			fmt.Fprintln(os.Stderr, "Unable to retrieve hook script")
			panic("Error")
		}

		_, err = io.Copy(script, resp.Body)
		resp.Body.Close()
		failIfError(err, "Error writing script file")
		err = script.Close()
		failIfError(err, "Error flushing script file")

		err = os.Chmod(script.Name(), 0755)
		failIfError(err, "Error changing script permissions")
	}
}

func bwrapGetBool(bwrap map[string]interface{}, opt string, required bool) bool {
	vali, exists := bwrap[opt]
	if !exists {
		if required {
			failNow("BWrap configuration invalid: " + opt + " not configured")
		}
		return false
	}
	val, ok := vali.(bool)
	if !ok {
		failNow("BWrap configuration invalid: " + opt + " not bool")
	}
	return val
}

func bwrapAddFakeStringMap(bwrap map[string]interface{}, opt string, cmd []string) []string {
	argname := "--" + strings.Replace(opt, "_", "-", -1)
	vali, exists := bwrap[opt]
	if !exists || vali == nil {
		return cmd
	}
	val, ok := vali.([]interface{})
	if !ok {
		failNow("BWrap configuration invalid: " + opt + " not fake string map")
	}
	for _, valuei := range val {
		value, ok := valuei.([]interface{})
		if !ok {
			failNow("BWrap configuration invalid: " + opt + " not string map1")
		}
		if len(value) != 2 {
			failNow("BWrap configuration invalid: " + opt + " has entry with invalid length")
		}
		key, ok := value[0].(string)
		if !ok {
			failNow("BWrap configuration invalid: " + opt + " has invalid entry")
		}
		keyval, ok := value[1].(string)
		if !ok {
			failNow("BWrap configuration invalid: " + opt + " has invalid entry")
		}
		cmd = append(
			cmd,
			argname,
			key,
			keyval,
		)
	}
	return cmd
}

func bwrapAddString(bwrap map[string]interface{}, opt string, cmd []string) []string {
	argname := "--" + opt
	vali, exists := bwrap[opt]
	if !exists || vali == nil {
		return cmd
	}
	val, ok := vali.(string)
	if !ok {
		failNow("BWrap configuration invalid: " + opt + " not string")
	}
	return append(
		cmd,
		argname,
		val,
	)
}

func bwrapAddInt(bwrap map[string]interface{}, opt string, cmd []string) []string {
	argname := "--" + opt
	vali, exists := bwrap[opt]
	if !exists || vali == nil {
		return cmd
	}
	val, ok := vali.(int)
	if !ok {
		failNow("BWrap configuration invalid: " + opt + " not integer")
	}
	return append(
		cmd,
		argname,
		strconv.Itoa(val),
	)
}

func getBwrapConfig(bwrap map[string]interface{}) (usebwrap bool, cmd []string) {
	cmd = []string{}

	if !bwrapGetBool(bwrap, "enabled", true) {
		// BubbleWrap not enabled, we are done
		return
	}
	usebwrap = true

	unshare, has := bwrap["unshare"]
	if has {
		val, ok := unshare.([]interface{})
		if !ok {
			failNow("BWrap configuration invalid: unshare not list of strings")
		}
		for _, iopt := range val {
			opt, ok := iopt.(string)
			if !ok {
				failNow("BWrap configuration invalid: unshare not list of strings")
			}
			cmd = append(cmd, "--unshare-"+opt)
		}
	}

	if bwrapGetBool(bwrap, "share_net", false) {
		cmd = append(cmd, "--share-net")
	}

	if bwrapGetBool(bwrap, "mount_proc", false) {
		cmd = append(cmd, "--proc", "/proc")
	}

	if bwrapGetBool(bwrap, "mount_dev", false) {
		cmd = append(cmd, "--dev", "/dev")
	}

	cmd = bwrapAddFakeStringMap(bwrap, "bind", cmd)
	cmd = bwrapAddFakeStringMap(bwrap, "ro_bind", cmd)
	cmd = bwrapAddFakeStringMap(bwrap, "symlink", cmd)
	cmd = bwrapAddString(bwrap, "hostname", cmd)
	cmd = bwrapAddInt(bwrap, "uid", cmd)
	cmd = bwrapAddInt(bwrap, "gid", cmd)

	return
}
