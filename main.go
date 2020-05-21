package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-github/v31/github"
	"golang.org/x/oauth2"
	"gopkg.in/pipe.v2"
	"gopkg.in/yaml.v2"
)

type State string

const (
	Unknown = State("unknown")
	Present = State("present")
	Absent  = State("absent")
)

type Profile string

var home = "/root" // os.Getenv("HOME")

/*
type Profile struct {
	Name string `json:"name,omitempty"`
	State State `json:"state,omitempty"`
	URL  string `json:"url,omitempty"`
}
*/

func readDesiredProfiles() []Profile {
	profiles := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.ReadFile("cluster.yaml"),
		pipe.Exec("yq", "read", "-", "spec.profiles"),
		pipe.Tee(profiles),
	)); err != nil {
		return nil
	}

	result := make([]Profile, 0)
	err := yaml.Unmarshal(profiles.Bytes(), &result)
	if err != nil {
		return nil
	}

	return result
}

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

func getDesiredField(query string) string {
	value := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.Exec("yq", "read", "cluster.yaml", query),
		pipe.Tee(value),
	)); err != nil {
		return ""
	}

	return strings.TrimSpace(value.String())
}

func getDesiredRegion() string {
	return getDesiredField("spec.template.metadata.region")
}

func getDesiredClusterName() string {
	return getDesiredField("spec.template.metadata.name")
}

func getDesiredTimeout() string {
	timeout := getDesiredField("timeout")
	if timeout == "" {
		return "25m"
	}

	return timeout
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

	timeout := getDesiredTimeout()

	cmd := exec.Command("eksctl", "create", "--timeout", timeout, "cluster", "-f", "-")
	cmd.Stdin = strings.NewReader(clusterConfig.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Executing: %s %s %s\n", cmd.Env, cmd.Path, strings.Join(cmd.Args, " "))
	return cmd.Run()
}

func deleteCluster() error {
	name := getDesiredClusterName()
	cmd := exec.Command("eksctl", "delete", "cluster", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Executing: %s %s %s\n", cmd.Env, cmd.Path, strings.Join(cmd.Args, " "))
	return cmd.Run()
}

func writeKubeConfig() error {
	name := getDesiredClusterName()
	cmd := exec.Command("eksctl", "utils", "write-kubeconfig", "--cluster", name)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Executing: %s %s %s\n", cmd.Env, cmd.Path, strings.Join(cmd.Args, " "))
	return cmd.Run()
}

func enableGitOpsRepository(envs []string) error {
	privateKeyPath := filepath.Join(home, ".ssh", "id_rsa")

	clusterName := getDesiredClusterName()
	region := getDesiredRegion()
	gitopsRepo := "git@github.com:" + os.Getenv("GITHUB_REPOSITORY")

	cmd := exec.Command("eksctl", "enable", "repo",
		"--verbose=4",
		"--git-url="+gitopsRepo,
		"--git-email=flux@noreply.gitops",
		"--git-private-ssh-key-path="+privateKeyPath,
		"--cluster="+clusterName,
		"--region="+region)

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "EKSCTL_EXPERIMENTAL=true")
	cmd.Env = append(cmd.Env, envs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Executing: %s %s %s\n", cmd.Env, cmd.Path, strings.Join(cmd.Args, " "))
	return cmd.Run()
}

func enableProfile(envs []string, profile Profile) error {
	privateKeyPath := filepath.Join(home, ".ssh", "id_rsa")

	clusterName := getDesiredClusterName()
	region := getDesiredRegion()
	gitopsRepo := "git@github.com:" + os.Getenv("GITHUB_REPOSITORY")

	cmd := exec.Command("eksctl", "enable", "profile",
		"--verbose=4",
		"--git-url="+gitopsRepo,
		"--git-email=flux@noreply.gitops",
		"--git-private-ssh-key-path="+privateKeyPath,
		"--cluster="+clusterName,
		"--region="+region,
		string(profile))

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "EKSCTL_EXPERIMENTAL=true")
	cmd.Env = append(cmd.Env, envs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runSshAgent() []string {
	output := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.Exec("ssh-agent", "-s"),
		pipe.Filter(func(line []byte) bool {
			s := string(line)
			return strings.HasPrefix(s, "SSH_")
		}),
		pipe.Replace(func(line []byte) []byte {
			s := string(line)
			parts := strings.SplitN(s, ";", 2)
			return []byte(parts[0] + "\n")
		}),
		pipe.Tee(output),
	)); err != nil {
		log.Fatal(err)
	}

	env := strings.Split(output.String(), "\n")
	return env
}

func getDeployKeyFromFlux() string {
	key := &bytes.Buffer{}
	if err := pipe.Run(pipe.Line(
		pipe.Exec("fluxctl", "--k8s-fwd-ns=flux", "identity"),
		pipe.Tee(key),
	)); err != nil {
		return ""
	}
	return strings.TrimSpace(key.String())
}

func addOrUpdateDeployKey(title, key string) (*github.Key, error) {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		return nil, errors.New("expected GH_TOKEN")
	}
	githubRepository := os.Getenv("GITHUB_REPOSITORY")
	if githubRepository == "" {
		return nil, errors.New("expected GITHUB_REPOSITORY")
	}

	parts := strings.SplitN(githubRepository, "/", 2)
	if len(parts) != 2 {
		return nil, errors.New("expected repo in the form of owner/repo")
	}
	owner := parts[0]
	repo := parts[1]

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	authClient := oauth2.NewClient(context.Background(), tokenSource)
	client := github.NewClient(authClient)

	keys, _, err := client.Repositories.ListKeys(context.Background(), owner, repo, nil)
	if err != nil {
		return nil, err
	}

	for _, k := range keys {
		if *k.Title == title {
			if _, err := client.Repositories.DeleteKey(context.Background(), owner, repo, *k.ID); err != nil {
				return nil, err
			}
			break
		}
	}

	k, _, err := client.Repositories.CreateKey(context.Background(), owner, repo, &github.Key{
		Key:      github.String(key),
		Title:    github.String(title),
		ReadOnly: github.Bool(false),
	})

	if err != nil {
		return nil, err
	}

	return k, nil
}

func deleteDeployKey(key *github.Key) error {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		return errors.New("expected GH_TOKEN")
	}
	githubRepository := os.Getenv("GITHUB_REPOSITORY")
	if githubRepository == "" {
		return errors.New("expected GITHUB_REPOSITORY")
	}

	parts := strings.SplitN(githubRepository, "/", 2)
	if len(parts) != 2 {
		return errors.New("expected repo in the form of owner/repo")
	}
	owner := parts[0]
	repo := parts[1]

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	authClient := oauth2.NewClient(context.Background(), tokenSource)
	client := github.NewClient(authClient)

	if _, err := client.Repositories.DeleteKey(context.Background(), owner, repo, *key.ID); err != nil {
		return err
	}

	return nil
}

func generateKeyAndAllowDeployKey(envs []string) (*github.Key, error) {
	err := os.MkdirAll(filepath.Join(home, ".ssh"), 0755)
	if err != nil {
		return nil, err
	}

	cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "4096", "-N", "", "-f", filepath.Join(home, ".ssh", "id_rsa"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		return nil, err
	}

	knownHosts := `
github.com ssh-rsa AAAAB3NzaC1yc2EAAAABIwAAAQEAq2A7hRGmdnm9tUDbO9IDSwBK6TbQa+PXYPCPy6rbTrTtw7PHkccKrpp0yVhp5HdEIcKr6pLlVDBfOLX9QUsyCOV0wzfjIJNlGEYsdlLJizHhbn2mUjvSAHQqZETYP81eFzLQNnPHt4EVVUh7VfDESU84KezmD5QlWpXLmvU31/yMf+Se8xhHTvKSCZIFImWwoG6mbUoWf9nzpIoaSjB+weqqUUmpaaasXVal72J+UX2B+2RPW3RcT0eOzQgqlJL3RKrTJvdsjE3JEAvGq3lGHSZXy28G3skua2SmVi/w4yCE6gbODqnTWlg7+wC604ydGXA8VJiS5ap43JXiUFFAaQ==
github.com ssh-dss AAAAB3NzaC1kc3MAAACBANGFW2P9xlGU3zWrymJgI/lKo//ZW2WfVtmbsUZJ5uyKArtlQOT2+WRhcg4979aFxgKdcsqAYW3/LS1T2km3jYW/vr4Uzn+dXWODVk5VlUiZ1HFOHf6s6ITcZvjvdbp6ZbpM+DuJT7Bw+h5Fx8Qt8I16oCZYmAPJRtu46o9C2zk1AAAAFQC4gdFGcSbp5Gr0Wd5Ay/jtcldMewAAAIATTgn4sY4Nem/FQE+XJlyUQptPWMem5fwOcWtSXiTKaaN0lkk2p2snz+EJvAGXGq9dTSWHyLJSM2W6ZdQDqWJ1k+cL8CARAqL+UMwF84CR0m3hj+wtVGD/J4G5kW2DBAf4/bqzP4469lT+dF2FRQ2L9JKXrCWcnhMtJUvua8dvnwAAAIB6C4nQfAA7x8oLta6tT+oCk2WQcydNsyugE8vLrHlogoWEicla6cWPk7oXSspbzUcfkjN3Qa6e74PhRkc7JdSdAlFzU3m7LMkXo1MHgkqNX8glxWNVqBSc0YRdbFdTkL0C6gtpklilhvuHQCdbgB3LBAikcRkDp+FCVkUgPC/7Rw==
`
	if err := ioutil.WriteFile(filepath.Join(home, ".ssh", "known_hosts"), []byte(knownHosts), 0600); err != nil {
		return nil, err

	}

	if err := pipe.Run(pipe.Line(
		pipe.Exec("ssh-keyscan", "-t", "rsa", "github.com"),
		pipe.AppendFile(filepath.Join(home, ".ssh", "known_hosts"), 0600),
	)); err != nil {
		return nil, err

	}

	sshAdd := exec.Command("ssh-add", filepath.Join(home, ".ssh", "id_rsa"))
	sshAdd.Env = append(os.Environ(), envs...)
	sshAdd.Stdout = os.Stdout
	sshAdd.Stderr = os.Stderr
	err = sshAdd.Run()
	if err != nil {
		return nil, err

	}

	key, err := ioutil.ReadFile(filepath.Join(home, ".ssh", "id_rsa.pub"))
	if err != nil {
		return nil, err
	}

	str := RandomString(10)
	return addOrUpdateDeployKey("push-key-"+str, string(key))
}

// RandomString generates a random string of n length
func RandomString(n int) string {
	var characterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	b := make([]rune, n)
	for i := range b {
		b[i] = characterRunes[rand.Intn(len(characterRunes))]
	}
	return string(b)
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

	envs := runSshAgent()

	// if cluster is present
	// we enable gitops
	if getClusterState() == Present {
		fmt.Println("Generating Key ...")
		deployKey, err := generateKeyAndAllowDeployKey(envs)
		if err != nil {
			log.Fatal(err)
		}
		defer deleteDeployKey(deployKey)

		time.Sleep(5 * time.Second)
		fmt.Println("Enabling GitOps repository ...")
		enableGitOpsRepository(envs)

		fmt.Println("Getting deploy key from Flux ...")
		key := getDeployKeyFromFlux()

		fmt.Println("Adding deploy key to the repo ...")
		_, err = addOrUpdateDeployKey("flux", key)
		if err != nil {
			log.Fatal(err)
		}

	}

	// if cluster is present
	// we applying profiles
	if getClusterState() == Present {
		profiles := readDesiredProfiles()
		for _, profile := range profiles {
			enableProfile(envs, profile)
		}
	}

	fmt.Println("Verifying Cluster State ...")
	fmt.Printf("Cluster State: %q => Cluster Desired State: %q\n", getClusterState(), getClusterDesiredState())
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
