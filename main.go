package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
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

func enableGitOpsRepository() error {
	clusterName := getDesiredClusterName()
	region := getDesiredRegion()
	gitopsRepo := "git@github.com:" + os.Getenv("GITHUB_REPOSITORY")

	cmd := exec.Command("eksctl", "enable", "repo",
		"--git-url="+gitopsRepo,
		"--git-email=flux@noreply.gitops",
		"--cluster="+clusterName,
		"--region="+region)

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "EKSCTL_EXPERIMENTAL=true")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Printf("Executing: %s %s %s\n", cmd.Env, cmd.Path, strings.Join(cmd.Args, " "))
	return cmd.Run()
}

func enableProfile(profile Profile) error {
	clusterName := getDesiredClusterName()
	region := getDesiredRegion()
	gitopsRepo := "git@github.com:" + os.Getenv("GITHUB_REPOSITORY")

	cmd := exec.Command("eksctl", "enable", "profile",
		"--git-url="+gitopsRepo,
		"--git-email=flux@noreply.gitops",
		"--cluster="+clusterName,
		"--region="+region,
		string(profile))

	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "EKSCTL_EXPERIMENTAL=true")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
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

func addDeployKey(name, key string) error {
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

	_, _, err := client.Repositories.CreateKey(context.Background(), owner, repo, &github.Key{
		Key:      github.String(key),
		Title:    github.String(name),
		ReadOnly: github.Bool(false),
	})

	if err != nil {
		return err
	}

	return nil
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

	// if cluster is present
	// we enable gitops
	if getClusterState() == Present {
		fmt.Println("Generating Key ...")
		generateKeyAndAllowDeployKey()
		fmt.Println("Enabling GitOps repository ...")
		enableGitOpsRepository()

		fmt.Println("Getting deploy key from Flux ...")
		key := getDeployKeyFromFlux()

		fmt.Println("Adding deploy key to the repo ...")
		err := addDeployKey("flux", key)
		if err != nil {
			log.Fatal(err)
		}
	}

	// if cluster is present
	// we applying profiles
	if getClusterState() == Present {
		profiles := readDesiredProfiles()
		for _, profile := range profiles {
			enableProfile(profile)
		}
	}

	fmt.Println("Verifying Cluster State ...")
	fmt.Printf("Cluster State: %q => Cluster Desired State: %q\n", getClusterState(), getClusterDesiredState())
}

func generateKeyAndAllowDeployKey() error {
	cmd := exec.Command("ssh-keygen", "-t", "rsa", "-N", "''", "-f", "~/.ssh/id_rsa")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		return err
	}

	key, err := ioutil.ReadFile("~/.ssh/id_rsa.pub")
	if err != nil {
		return err
	}

	str := RandomString(10)
	return addDeployKey("push-key-"+str, string(key))
}

// NewSHA1Hash generates a new SHA1 hash based on
// a random number of characters.
func NewSHA1Hash(n ...int) string {
	noRandomCharacters := 32

	if len(n) > 0 {
		noRandomCharacters = n[0]
	}

	randString := RandomString(noRandomCharacters)

	hash := sha1.New()
	hash.Write([]byte(randString))
	bs := hash.Sum(nil)

	return fmt.Sprintf("%x", bs)
}

var characterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

// RandomString generates a random string of n length
func RandomString(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = characterRunes[rand.Intn(len(characterRunes))]
	}
	return string(b)
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

/*if profile.State == Present {
	enableProfile(profile.URL)
} else if profile.State == Absent {
	// disableProfile(profile.URL)
	fmt.Println("Not yet implemented")
}*/
