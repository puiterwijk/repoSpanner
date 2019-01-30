package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/pkg/errors"
	"github.com/spf13/viper"
	"repospanner.org/repospanner/server/constants"
	"repospanner.org/repospanner/server/datastructures"
	pb "repospanner.org/repospanner/server/protobuf"
	"repospanner.org/repospanner/server/storage"
)

type hookType string

const (
	hookTypePreReceive  hookType = "pre-receive"
	hookTypeUpdate      hookType = "update"
	hookTypePostReceive hookType = "post-receive"
)

type hookRunning interface {
	fetchFakeRefs() error
	runHook(hook hookType) error
	close()
}

type nullHookRunning struct{}

type localHookRunning struct {
	cmdnum  int
	proc    *exec.Cmd
	control *os.File
	reader  <-chan []byte
	waiter  <-chan error
}

func (n nullHookRunning) fetchFakeRefs() error {
	return nil
}

func (n nullHookRunning) runHook(hook hookType) error {
	return nil
}

func (n nullHookRunning) close() {}

func (r *localHookRunning) sendCommand(cmd constants.Command, arguments ...string) error {
	args := []string{cmd, arguments...}
	cmdstr := strings.Join(args, " ")
	// TODO: Send command, and wait for OK back
	return errors.New("sendCommand not implemented yet")
}

func (r *localHookRunning) fetchFakeRefs() error {
	return r.sendCommand(constants.CmdFetch)
}

func (r *localHookRunning) runHook(hook hookType) error {
	return r.sendCommand(constants.CmdRun, string(hook))
	_, err := r.control.Write([]byte(string(hook) + "\n"))
	if err != nil {
		return errors.Wrap(err, "Error writing hook control channel")
	}
	for {
		select {
		case msg := <-r.reader:
			if msg == nil {
				continue
			}
			msgS := string(msg)
			if msgS == "OK\n" {
				return nil
			} else if msgS == "FAIL\n" {
				return errors.New("Hook reported a failure")
			} else {
				return errors.Errorf("Invalid hook result: %s", msgS)
			}
		case err := <-r.waiter:
			return err
		}
	}
}

func (r *localHookRunning) close() {
	r.proc.Process.Kill()
	<-r.waiter
	r.control.Close()
}

func (cfg *Service) prepareHookRunning(errout, infoout io.Writer, projectname string, request *pb.PushRequest, extraEnv map[string]string) (hookRunning, error) {
	hooks := cfg.statestore.GetRepoHooks(projectname)
	if hooks.PreReceive == "" || hooks.Update == "" || hooks.PostReceive == "" {
		return nil, fmt.Errorf("Hook not configured??")
	}
	if storage.ObjectID(hooks.PreReceive) == storage.ZeroID && storage.ObjectID(hooks.Update) == storage.ZeroID && storage.ObjectID(hooks.PostReceive) == storage.ZeroID {
		return nullHookRunning{}, nil
	}

	// We have a hook to run! Let's prepare the request
	var err error
	var clientcacert = []byte{}
	if cfg.ClientCaCertFile != "" {
		clientcacert, err = ioutil.ReadFile(cfg.ClientCaCertFile)
		if err != nil {
			return nil, errors.Wrap(err, "Error preparing hook calling")
		}
	}
	clientcert, err := ioutil.ReadFile(cfg.ClientCertificate)
	if err != nil {
		return nil, errors.Wrap(err, "Error preparing hook calling")
	}
	clientkey, err := ioutil.ReadFile(cfg.ClientKey)
	if err != nil {
		return nil, errors.Wrap(err, "Error preparing hook calling")
	}

	hookreq := datastructures.HookRunRequest{
		Debug:        viper.GetBool("hooks.debug"),
		RPCURL:       cfg.findRPCURL(),
		PushUUID:     request.UUID(),
		BwrapConfig:  viper.GetStringMap("hooks.bubblewrap"),
		User:         viper.GetInt("hooks.user"),
		ProjectName:  projectname,
		HookObjects:  make(map[string]string),
		Requests:     make(map[string][2]string),
		ClientCaCert: string(clientcacert),
		ClientCert:   string(clientcert),
		ClientKey:    string(clientkey),
		ExtraEnv:     extraEnv,
	}
	for _, req := range request.Requests {
		reqt := [2]string{req.GetFrom(), req.GetTo()}
		hookreq.Requests[req.GetRef()] = reqt
	}

	// Add hook objects
	if storage.ObjectID(hooks.PreReceive) != storage.ZeroID {
		hookreq.HookObjects[string(hookTypePreReceive)] = hooks.PreReceive
	}
	if storage.ObjectID(hooks.Update) != storage.ZeroID {
		hookreq.HookObjects[string(hookTypeUpdate)] = hooks.Update
	}
	if storage.ObjectID(hooks.PostReceive) != storage.ZeroID {
		hookreq.HookObjects[string(hookTypePostReceive)] = hooks.PostReceive
	}

	// Marshal the request
	req, err := json.Marshal(hookreq)
	if err != nil {
		return nil, errors.Wrap(err, "Error preparing hook calling")
	}
	reqbuf := bytes.NewBuffer(req)

	// Start the local hook runner
	return cfg.runLocalHookBinary(errout, infoout, reqbuf)
}

func getSocketPair() (*os.File, *os.File, error) {
	socks, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}

	sockParent := os.NewFile(uintptr(socks[0]), "control_parent")
	sockChild := os.NewFile(uintptr(socks[1]), "control_child")
	if sockParent == nil || sockChild == nil {
		syscall.Close(socks[0])
		syscall.Close(socks[1])
		return nil, nil, errors.New("Error creating files for control channel")
	}

	return sockParent, sockChild, nil
}

func (cfg *Service) runLocalHookBinary(errout, infoout io.Writer, req io.Reader) (hookRunning, error) {
	binary := viper.GetString("hooks.runner")
	if binary == "" {
		return nil, errors.New("No hook runner configured")
	}
	cmd := exec.Command(
		binary,
	)
	cmd.Stdin = req
	cmd.Stdout = infoout
	cmd.Stderr = errout

	// Add control channels
	sockParent, sockChild, err := getSocketPair()
	if err != nil {
		return nil, errors.Wrap(err, "Error creating hook control channel")
	}
	cmd.ExtraFiles = []*os.File{sockChild}

	// Start the binary
	err = cmd.Start()
	if err != nil {
		return nil, errors.Wrap(err, "Error executing hook")
	}

	// Create the waiter
	waiter := make(chan error)
	go func() {
		waiter <- cmd.Wait()
		close(waiter)
	}()

	// Create the reader
	reader := make(chan []byte)
	go func() {
		buf := make([]byte, 10)
		_, err := sockParent.Read(buf)
		if err != nil {
			close(reader)
			return
		}
		reader <- buf
	}()

	// And now, just return all that goodness
	return &localHookRunning{
		proc:    cmd,
		control: sockParent,
		reader:  reader,
		waiter:  waiter,
	}, nil
}
