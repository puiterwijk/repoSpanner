package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"

	"github.com/pkg/errors"
	"github.com/spf13/viper"
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
	runHook(hook hookType) error
	close()
}

type nullHookRunning struct{}

type localHookRunning struct {
	proc           *exec.Cmd
	controlSend    *os.File
	controlReceive *os.File
}

func (n nullHookRunning) runHook(hook hookType) error {
	return nil
}

func (n nullHookRunning) close() {}

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

func (r *localHookRunning) runHook(hook hookType) error {
	return errors.New("Hook running not implemented")
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
	control

	err := cmd.Run()
	if err != nil {
		return nil, errors.Wrap(err, "Error executing hook")
	}

	return nil, errors.New("Not implemented")
}
