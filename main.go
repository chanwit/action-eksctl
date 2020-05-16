package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/pipe.v2"
)

type State string

const (
	Unknown = State("UNKNOWN")
	Present = State("PRESENT")
	Absent  = State("ABSENT")
)

func getClusterDesiredState() State {
	state := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.ReadFile("cluster.yaml"),
		pipe.Exec("yq", "read", "-", "spec.state"),
		pipe.Tee(state),
	)); err != nil {
		return Unknown
	}

	switch strings.TrimSpace(state.String()) {
	case "present":
		return Present
	case "absent":
		return Absent
	}

	return Unknown
}

func getDesiredClusterName() string {

	clusterName := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.Exec("yq", "read", "cluster.yaml", "spec.template.metadata.name"),
		pipe.Tee(clusterName),
	)); err != nil {
		return ""
	}

	return strings.TrimSpace(clusterName.String())
}

func getClusterState() State {
	name := getDesiredClusterName()
	if name == "" {
		return Unknown
	}
	return getClusterStateByName(name)
}

func getClusterStateByName(name string) State {
	clusterName := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.Exec("eksctl", "get", "cluster", "-o", "yaml"),
		pipe.Exec("yq", "read", "-", ".name"),
		pipe.Filter(func(line []byte) bool {
			return string(line) == name
		}),
		pipe.Tee(clusterName),
	)); err != nil {
		return Unknown
	}

	if strings.TrimSpace(clusterName.String()) == name {
		return Present
	} else {
		return Absent
	}
}

func createCluster() error {
	clusterConfig := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.Exec("yq", "read", "cluster.yaml", "spec.template"),
		pipe.Tee(clusterConfig),
	)); err != nil {
		return err
	}

	cmd := exec.Command("eksctl", "create", "cluster", "-f", "-")
	cmd.Stdin = strings.NewReader(clusterConfig.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func deleteCluster() error {
	name := getDesiredClusterName()
	cmd := exec.Command("eksctl", "delete", "cluster", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func writeKubeConfig() error {
	name := getDesiredClusterName()
	cmd := exec.Command("eksctl", "utils", "write-kubeconfig", "--cluster", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {

	clusterDesiredState := getClusterDesiredState()
	clusterState := getClusterState()
	fmt.Printf("Cluster State: %q => Cluster Desired State: %q ...\n", clusterState, clusterDesiredState)

	switch clusterState {
	case Absent:
		if clusterDesiredState == Present {
			fmt.Println("Creating Cluster ...")
			createCluster()
		} else if clusterDesiredState == Absent {
			// nothing to do
			fmt.Println("Do nothing.")
		}

	case Present:
		if clusterDesiredState == Absent {
			fmt.Println("Deleting Cluster ...")
			for {
				deleteCluster()
				if getClusterState() == Absent {
					break
				}
				fmt.Println("Waiting for 30s")
				time.Sleep(30 * time.Second)
				fmt.Println("Cluster still here. Keep deleting ...")
			}
		} else if clusterDesiredState == Present {
			// update
			fmt.Println("NYI: Should update cluster")

			fmt.Println("Writing KubeConfig ...")
			writeKubeConfig()
		}
	}

	fmt.Println("Verifying Cluster State ...")
	fmt.Printf("Cluster State: %q => Cluster Desired State: %q\n", getClusterState(), getClusterDesiredState())
}
