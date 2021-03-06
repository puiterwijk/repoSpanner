package storage

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"
)

func testStorageDriver(name string, instance StorageDriver, t *testing.T) {
	// No objects stored yet. This should ay NoSuchObjectError
	p1 := instance.GetProjectStorage("test/project1")
	_, _, _, err := p1.ReadObject("object1")
	if err != ErrObjectNotFound {
		t.Fatal("New Object ID did not return expected error")
	}

	// Write a first object
	p1w := p1.GetPusher("")
	staged, err := p1w.StageObject(ObjectTypeBlob, 4)
	if err != nil {
		t.Fatal(fmt.Sprintf("StageObject returned error: %s", err))
	}
	n, err := staged.Write([]byte("test"))
	if err != nil {
		t.Fatal(fmt.Sprintf("Staged write returned error: %s", err))
	}
	if n != 4 {
		t.Fatal(fmt.Sprintf("Staged writed did not write expected number of bytes: %d", n))
	}
	_, err = staged.Finalize("30d74d258442c7c65512eafab474568dd706c430")
	if err != nil {
		t.Fatal(fmt.Sprintf("Unexpected error from finalizing: %s", err))
	}
	err = staged.Close()
	if err != nil {
		t.Fatal(fmt.Sprintf("Unexpected error on close: %s", err))
	}
	doneC := p1w.GetPushResultChannel()
	select {
	case err := <-doneC:
		t.Fatal(fmt.Sprintf("Unexpected error on close channel: %s", err))
	default:
		// No message is good here
		break
	}
	p1w.Done()
	select {
	case err, isopen := <-doneC:
		if err != nil {
			t.Fatal(fmt.Sprintf("Unexpected error when we expected close: %s", err))
		}
		if !isopen {
			// Channel closed correctly
			break
		}
	default:
		t.Fatal("Done channel not closed")
	}

	// Re-write the first object
	p1w = p1.GetPusher("")
	staged, err = p1w.StageObject(ObjectTypeBlob, 4)
	if err != nil {
		t.Fatal(fmt.Sprintf("StageObject returned error: %s", err))
	}
	n, err = staged.Write([]byte("test"))
	if err != nil {
		t.Fatal(fmt.Sprintf("Staged write returned error: %s", err))
	}
	if n != 4 {
		t.Fatal(fmt.Sprintf("Staged writed did not write expected number of bytes: %d", n))
	}
	_, err = staged.Finalize("30d74d258442c7c65512eafab474568dd706c430")
	if err != nil {
		t.Fatal(fmt.Sprintf("Unexpected error from finalizing: %s", err))
	}
	err = staged.Close()
	if err != nil {
		t.Fatal(fmt.Sprintf("Unexpected error on close: %s", err))
	}
	doneC = p1w.GetPushResultChannel()
	select {
	case err := <-doneC:
		t.Fatal(fmt.Sprintf("Unexpected error on close channel: %s", err))
	default:
		// No message is good here
		break
	}
	p1w.Done()
	select {
	case err, isopen := <-doneC:
		if err != nil {
			t.Fatal(fmt.Sprintf("Unexpected error when we expected close: %s", err))
		}
		if !isopen {
			// Channel closed correctly
			break
		}
	default:
		t.Fatal("Done channel not closed")
	}

	staged, err = p1w.StageObject(ObjectTypeBlob, 5)
	if err != nil {
		t.Fatal(fmt.Sprintf("StageObject returned error: %s", err))
	}
	n, err = staged.Write([]byte("test1"))
	if err != nil {
		t.Fatal(fmt.Sprintf("Staged write returned error: %s", err))
	}
	if n != 5 {
		t.Fatal(fmt.Sprintf("Staged writed did not write expected number of bytes: %d", n))
	}
	_, err = staged.Finalize("30d74d258442c7c65512eafab474568dd706c430")
	if err.Error() != "Calculated object does not match provided: f079749c42ffdcc5f52ed2d3a6f15b09307e975e != 30d74d258442c7c65512eafab474568dd706c430" {
		t.Fatal(fmt.Sprintf("Unexpected error returned on close: %s", err))
	}
	err = staged.Close()
	if err != nil {
		t.Fatal(fmt.Sprintf("Unexpected error on close: %s", err))
	}

	objtype, objsize, objr, err := p1.ReadObject("30d74d258442c7c65512eafab474568dd706c430")
	if err != nil {
		t.Fatal(fmt.Sprintf("Error when getting just-written object: %s", err))
	}
	if objsize != 4 {
		t.Fatal(fmt.Sprintf("Incorrect number of bytes returned: %d != 4", objsize))
	}
	if objtype != ObjectTypeBlob {
		t.Fatal(fmt.Sprintf("Incorrect object type returned: %s != ObjectTypeBlob", objtype))
	}
	buf := make([]byte, 6)
	n, err = objr.Read(buf)
	if err != nil {
		t.Fatal(fmt.Sprintf("Error reading object: %s", err))
	}
	if n != 4 {
		t.Fatal(fmt.Sprintf("Unexpected number of bytes read: %d", n))
	}
	if buf[0] != 't' || buf[1] != 'e' || buf[2] != 's' || buf[3] != 't' {
		t.Fatal(fmt.Sprintf("Incorrect object returned: %s", buf))
	}
	err = objr.Close()
	if err != nil {
		t.Fatal(fmt.Sprintf("Error when closing read object: %s", err))
	}
}

func TestTreeStorageDriver(t *testing.T) {
	dir, err := ioutil.TempDir("", "repospanner_test_")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	t.Run("tree", func(t *testing.T) {
		treeconf := map[string]string{}
		treeconf["type"] = "tree"
		treeconf["directory"] = dir
		treeconf["clustered"] = "false"
		instance, err := InitializeStorageDriver(treeconf)
		if err != nil {
			panic(err)
		}
		testStorageDriver("tree", instance, t)
	})
}
