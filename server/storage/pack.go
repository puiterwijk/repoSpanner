package storage

import (
	"github.com/pkg/errors"
)

type packStorageDriverInstance struct {
	dirname      string
	crossProject bool
}

func newPackStoreDriver(dirname string, crossproject bool) (*packStorageDriverInstance, error) {
	return nil, errors.New("packstore in progress")
}

func (d *packStorageDriverInstance) GetProjectStorage(project string) ProjectStorageDriver {
	return nil
}
