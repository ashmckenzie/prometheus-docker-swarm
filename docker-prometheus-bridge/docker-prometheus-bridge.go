package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"
	"encoding/json"
	"crypto/md5"
	"io/ioutil"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

/*
	{
		"targets": [
			"10.0.0.23:80"
		],
		"labels": {
			"job": "html2pdf"
		}
	}
*/
type PromServiceTargetsList struct {
    Targets []string `json:"targets"`
    Labels map[string]string `json:"labels"`
}

type PromServiceTargetsFile []PromServiceTargetsList

// TODO: parsing support is sucky
// TODO: does not yet support parsing the actual path
func parseMetricsEndpointEnv(envs []string) (bool, int, string) {
	for _, env := range envs {
		match, err := regexp.MatchString("^METRICS_ENDPOINT=.+", env)
		if err != nil {
			panic(err)
		}

		if (match) {
			portRe := regexp.MustCompile(":[0-9]+")

			port := portRe.FindString(env)
			if port == "" {
				port = ":80"
			}

			portI, err := strconv.Atoi(port[1:]) // ":80" => 80
			if err != nil {
				panic(err)
			}

			return true, portI, "/metrics"
		}
	}

	return false, 0, "" // hasMetricsEndpoint, metricsPort, metricsPath
}

// for some reason the ips contain a netmask like this "10.0.0.7/24"
func extractIp(mangledIp string) string {
	re := regexp.MustCompile("^[0-9\\.]+")

	ip := re.FindString(mangledIp)

	return ip
}

func syncFromDockerSwarm(cli *client.Client, previousHash string) (string, error) {
	// list services

	services, err := cli.ServiceList(context.Background(), types.ServiceListOptions{})
	if err != nil {
		return previousHash, err
	}

	serviceById := map[string]swarm.Service{}

	for _, service := range services {
		serviceById[ service.ID ] = service
	}

	// list tasks

	tasks, err := cli.TaskList(context.Background(), types.TaskListOptions{})
	if err != nil {
		return previousHash, err
	}

	serviceAddresses := make(map[string][]string)

	for _, task := range tasks {
		
		// TODO: this filter could probably be done with the TaskList() call more efficiently?
		if (task.Status.State != swarm.TaskStateRunning) {
			continue
		}

		hasMetricsEndpoint, metricsPort, _ := parseMetricsEndpointEnv(task.Spec.ContainerSpec.Env)

		if (!hasMetricsEndpoint) {
			continue
		}

		if (len(task.NetworksAttachments) > 0 && len(task.NetworksAttachments[0].Addresses) > 0) {
			ip := extractIp(task.NetworksAttachments[0].Addresses[0])

			taskServiceName := serviceById[ task.ServiceID ].Spec.Name

			serviceAddresses[ taskServiceName ] = append(serviceAddresses[ taskServiceName ], ip + ":" + strconv.Itoa(metricsPort))
		}
	}

	promServiceTargetsFileContent := PromServiceTargetsFile{}

	for serviceId, addresses := range serviceAddresses {
		labels := map[string]string{
			"job": serviceId,
		}

		serviceTarget := PromServiceTargetsList{addresses, labels}

		promServiceTargetsFileContent = append(promServiceTargetsFileContent, serviceTarget)		
	}

	promServiceTargetsFileContentJson, err := json.MarshalIndent(promServiceTargetsFileContent, "", "    ")
	if err != nil {
		panic(err)
	}

	newHash := fmt.Sprintf("%x", md5.Sum(promServiceTargetsFileContentJson))

	if newHash != previousHash {
		err := ioutil.WriteFile("/etc/prometheus/targets-from-swarm.json", promServiceTargetsFileContentJson, 0755)

		if err != nil {
			fmt.Println("PromServiceTargetsFile changed, write /etc/prometheus/targets-from-swarm.json FAILED:", err)
		} else {
			fmt.Println("PromServiceTargetsFile changed, wrote /etc/prometheus/targets-from-swarm.json")
		}
	} else {
		fmt.Println("No changes")
	}

	return newHash, nil
}

func main() {
	var cli *client.Client

	previousHash := ""

	for {
		var err error

		if cli == nil {
			// connection errors are not actually handled here, but instead when we call our first method.
			// but we do this just to be safe.
			cli, err = client.NewClient("unix:///var/run/docker.sock", "", nil, nil)
			if err != nil {
				fmt.Println("Failed connecting to Docker; re-trying in 5 seconds:", err)
				time.Sleep(5 * time.Second)
				continue
			}
		}

		previousHash, err = syncFromDockerSwarm(cli, previousHash)
		if err != nil {
			fmt.Println("syncFromDockerSwarm failed:", err)
		}

		time.Sleep(5 * time.Second)
	}
}