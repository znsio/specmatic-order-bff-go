package test

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/tidwall/gjson"
)

func StartDomainService(t *testing.T, env *TestEnvironment) (testcontainers.Container, string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("Error getting current directory: %v", err)

	}

	port, err := nat.NewPort("tcp", env.Config.BackendPort)
	if err != nil {
		return nil, "", fmt.Errorf("invalid port number: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image:        "znsio/specmatic",
		ExposedPorts: []string{port.Port() + "/tcp"},
		Networks: []string{
			env.BffTestNetwork.Name,
		},
		Cmd: []string{"stub"},
		Mounts: testcontainers.Mounts(
			testcontainers.BindMount(filepath.Join(pwd, "specmatic.yaml"), "/usr/src/app/specmatic.yaml"),
		),
		NetworkAliases: map[string][]string{
			env.BffTestNetwork.Name: {env.Config.BackendHost},
		},
		WaitingFor: wait.ForLog("Stub server is running"),
	}

	t.Log("Domain Container created")

	backendC, err := testcontainers.GenericContainer(env.Ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}

	mappedPort, err := backendC.MappedPort(env.Ctx, port)
	if err != nil {
		return nil, "", err
	}

	return backendC, mappedPort.Port(), nil
}

func StartKafkaMock(t *testing.T, env *TestEnvironment) (testcontainers.Container, string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, "", fmt.Errorf("Error getting current directory: %v", err)
	}

	port, err := nat.NewPort("tcp", env.Config.KafkaPort)
	if err != nil {
		return nil, "", fmt.Errorf("invalid port number: %w", err)
	}

	networkName := env.BffTestNetwork.Name

	req := testcontainers.ContainerRequest{
		Image:        "znsio/specmatic-kafka-trial:0.22.7-local",
		ExposedPorts: []string{port.Port() + "/tcp", env.Config.KafkaAPIPort + "/tcp"},
		Networks: []string{
			networkName,
		},
		NetworkAliases: map[string][]string{
			networkName: {env.Config.KafkaHost},
		},
		Cmd: []string{"--config=/specmatic.yaml", "--mock-server-api-port=" + env.Config.KafkaAPIPort},
		Mounts: testcontainers.Mounts(
			testcontainers.BindMount(filepath.Join(pwd, "specmatic.yaml"), "/usr/src/app/specmatic.yaml"),
		),
		Env: map[string]string{
			"KAFKA_EXTERNAL_HOST": env.Config.KafkaHost,
			"KAFKA_EXTERNAL_PORT": env.Config.KafkaPort,
		},
		WaitingFor: wait.ForLog("Listening on topics: (product-queries)").WithStartupTimeout(2 * time.Minute),
	}

	kafkaC, err := testcontainers.GenericContainer(env.Ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		fmt.Printf("Error starting Kafka mock container: %v", err)
	}

	mappedPort, err := kafkaC.MappedPort(env.Ctx, port)
	if err != nil {
		fmt.Printf("Error getting mapped port for Kafka mock: %v", err)
	}

	mappedApiPort, err := kafkaC.MappedPort(env.Ctx, nat.Port(env.Config.KafkaAPIPort))
	if err != nil {
		fmt.Printf("Error getting API server port: %v", err)
	} else {
		env.KafkaDynamicAPIPort = mappedApiPort.Port()
	}

	// Get the host IP
	kafkaAPIHost, err := kafkaC.Host(env.Ctx)
	if err != nil {
		return nil, "", fmt.Errorf("Error getting host IP: %v", err)
	}
	env.KafkaAPIHost = kafkaAPIHost

	if err := SetKafkaExpectations(env); err != nil {
		fmt.Printf("failed to set Kafka expectations ==== : %v", err)
	}

	return kafkaC, mappedPort.Port(), nil
}

func StartBFFService(t *testing.T, env *TestEnvironment) (testcontainers.Container, string, error) {

	port, err := nat.NewPort("tcp", env.Config.BFFServerPort)
	if err != nil {
		return nil, "", fmt.Errorf("invalid port number: %w", err)
	}

	networkName := env.BffTestNetwork.Name
	dockerfilePath := "Dockerfile"
	contextPath := "."

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    contextPath,
			Dockerfile: dockerfilePath,
		},
		Env: map[string]string{
			"DOMAIN_SERVER_PORT": env.Config.BackendPort,
			"DOMAIN_SERVER_HOST": env.Config.BackendHost,
			"KAFKA_PORT":         env.Config.KafkaPort,
			"KAFKA_HOST":         env.Config.KafkaHost,
		},
		ExposedPorts: []string{port.Port() + "/tcp"},
		Networks: []string{
			env.BffTestNetwork.Name,
		},
		WaitingFor: wait.ForLog("Listening and serving"),
		NetworkAliases: map[string][]string{
			networkName: {"bff-service"},
		},
	}

	bffContainer, err := testcontainers.GenericContainer(env.Ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", err
	}

	dynamicBffPort, err := bffContainer.MappedPort(env.Ctx, port)
	if err != nil {
		return nil, "", err
	}

	return bffContainer, dynamicBffPort.Port(), nil
}

func RunTestContainer(env *TestEnvironment) (string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("Error getting current directory: %v", err)
	}

	bffPortInt, err := strconv.Atoi(env.Config.BFFServerPort)
	if err != nil {
		return "", fmt.Errorf("invalid port number: %w", err)
	}

	req := testcontainers.ContainerRequest{
		Image: "znsio/specmatic",
		Env: map[string]string{
			"SPECMATIC_GENERATIVE_TESTS": "true",
		},
		Cmd: []string{"test", fmt.Sprintf("--port=%d", bffPortInt), "--host=bff-service"},
		Mounts: testcontainers.Mounts(
			testcontainers.BindMount(filepath.Join(pwd, "specmatic.yaml"), "/usr/src/app/specmatic.yaml"),
		),
		Networks: []string{
			env.BffTestNetwork.Name,
		},
		WaitingFor: wait.ForLog("Passed Tests:"),
	}

	testContainer, err := testcontainers.GenericContainer(env.Ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})

	if err != nil {
		return "", err
	}

	// Terminate test container post completion
	defer testContainer.Terminate(env.Ctx)

	// Streaming testing logs to terminal
	logReader, err := testContainer.Logs(env.Ctx)
	if err != nil {
		return "", err
	}
	defer logReader.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, logReader)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

func SetKafkaExpectations(env *TestEnvironment) error {
	endpoint := "/_expectations"
	url := fmt.Sprintf("http://%s:%s%s", env.KafkaAPIHost, env.KafkaDynamicAPIPort, endpoint)

	postBody := []byte(fmt.Sprintf(`[{"topic": "product-queries", "count": %d}]`, env.ExpectedMessageCount))

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(postBody))
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		return nil
	}

	fmt.Println(string(body))
	return nil
}

func VerifyKafkaExpectations(env *TestEnvironment) error {
	verificationUrl := fmt.Sprintf("http://%s:%s/%s", env.KafkaAPIHost, env.KafkaDynamicAPIPort, "_expectations/verifications")

	resp, err := http.Post(verificationUrl, "application/json", nil)
	if err != nil {
		fmt.Println("Error making request:", err)
		return nil
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error reading response:", err)
		return nil
	}

	result := gjson.ParseBytes(body)

	success := result.Get("success").Bool()
	errors := result.Get("errors").Array()

	if !success {
		errorMessages := make([]string, len(errors))
		for i, err := range errors {
			errorMessages[i] = err.String()
		}
		return fmt.Errorf("%v", errorMessages)
	}

	fmt.Println("Kafka mock expectations were met successfully.")
	return nil
}
