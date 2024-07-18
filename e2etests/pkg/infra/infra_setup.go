// SPDX-License-Identifier:Apache-2.0

package infra

import (
	"context"
	"fmt"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"errors"

	"go.universe.tf/e2etest/pkg/container"
	"go.universe.tf/e2etest/pkg/executor"
	frrconfig "go.universe.tf/e2etest/pkg/frr/config"
	frrcontainer "go.universe.tf/e2etest/pkg/frr/container"
	clientset "k8s.io/client-go/kubernetes"
)

const (
	kindNetwork         = "kind"
	vrfNetwork          = "vrf-net"
	DefaultVRFName      = ""
	VRFName             = "red"
	defaultRoutingTable = ""
	FRRK8sASN           = 64512
	FRRK8sASNVRF        = 64513
	externalASN         = 4200000000
)

var VRFs = []string{"", VRFName}

type nextHopSettings struct {
	nodeNetwork          string
	multiHopNetwork      string
	nodeRoutingTable     string
	nextHopContainerName string
}

var (
	hostIPv4      string
	hostIPv6      string
	FRRContainers []*frrcontainer.FRR

	vrfNextHopSettings = nextHopSettings{
		nodeNetwork:          vrfNetwork,
		multiHopNetwork:      "vrf-multihop",
		nodeRoutingTable:     "2",
		nextHopContainerName: "ebgp-vrf-single-hop",
	}
	defaultNextHopSettings = nextHopSettings{
		nodeNetwork:          kindNetwork,
		multiHopNetwork:      "kind-multihop",
		nodeRoutingTable:     defaultRoutingTable,
		nextHopContainerName: "ebgp-single-hop",
	}
)

func init() {
	if ip := os.Getenv("PROVISIONING_HOST_EXTERNAL_IPV4"); len(ip) != 0 {
		hostIPv4 = ip
	}
	if ip := os.Getenv("PROVISIONING_HOST_EXTERNAL_IPV6"); len(ip) != 0 {
		hostIPv6 = ip
	}
}

/*
This setup function is called when the test suite is provided with existing frr containers.
The caller calls the suite with a comma separated list of containers, which must be named after
the four ibgp/ebpg/single/multi hop containers.
In this case the test suite leverages those containers by only configuring them,
instead of creating new ones.
A common use case is to validate a real cluster that doesn't offer the luxury of configuring
the way the containers are connected to the cluster.
*/
func ExternalContainersSetup(externalContainers string, cs *clientset.Clientset) ([]*frrcontainer.FRR, error) {
	err := validateContainersNames(externalContainers)
	if err != nil {
		return nil, err
	}
	names := strings.Split(externalContainers, ",")
	configs := externalContainersConfigs()
	toApply := make(map[string]frrcontainer.Config)
	for _, n := range names {
		if c, ok := configs[n]; ok {
			toApply[n] = c
		}
	}

	res, err := frrcontainer.ConfigureExisting(toApply)
	if err != nil {
		return nil, err
	}

	if containsMultiHop(res) {
		err = multiHopSetUp(res, defaultNextHopSettings, cs)
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

func HostContainerSetup(image string) ([]*frrcontainer.FRR, error) {
	config := hostnetContainerConfig(image)
	res, err := frrcontainer.Create(config)
	if err != nil {
		return nil, err
	}
	return res, nil
}

/*
	When leveraging the kind network we spin up a total of 4 containers:
	  * ibgp container that uses the first IP, a single-hop away from our nodes (1st).
	  * ebgp container that uses the second IP, a single-hop away from our nodes,
	    and is connected to another containers network "multi-hop-net" (2nd).
	  * two ibgp/ebgp containers connected to the "multi-hop-net", multi-hops away
	    from our nodes (3rd,4th).
	We then wire these networks by adding static routes to both the nodes
	containers (we're using kind) and the ibgp/ebgp containers connected to multi-hop-net,
	using the 2nd container as a gateway.

*/

func KindnetContainersSetup(cs *clientset.Clientset, image string) ([]*frrcontainer.FRR, error) {
	configs := frrContainersConfigs(image)

	var out string
	out, err := executor.Host.Exec(executor.ContainerRuntime, "network", "create", defaultNextHopSettings.multiHopNetwork, "--ipv6",
		"--driver=bridge", "--subnet=172.30.0.0/16", "--subnet=fc00:f853:ccd:e798::/64")
	if err != nil && !strings.Contains(out, "already exists") {
		return nil, errors.Join(err, fmt.Errorf("failed to create %s: %s", defaultNextHopSettings.multiHopNetwork, out))
	}

	containers, err := frrcontainer.Create(configs)
	if err != nil {
		return nil, err
	}

	err = multiHopSetUp(containers, defaultNextHopSettings, cs)
	if err != nil {
		return nil, err
	}
	return containers, nil
}

/*
	In order to test MetalLB's announcement via VRFs, we:

	* create an additional "vrf-net" docker network
	* for each node, create a vrf named "red" and move the interface in that vrf
	* create a new frr container belonging to that network
	* by doing so, the frr container is reacheable only from "inside" the vrf
*/

func VRFContainersSetup(cs *clientset.Clientset, image string) ([]*frrcontainer.FRR, error) {
	out, err := executor.Host.Exec(executor.ContainerRuntime, "network", "create", vrfNetwork, "--ipv6",
		"--driver=bridge", "--subnet=172.31.0.0/16", "--subnet=fc00:f853:ccd:e799::/64")
	if err != nil && !strings.Contains(out, "already exists") {
		return nil, errors.Join(err, fmt.Errorf("failed to create %s: %s", vrfNetwork, out))
	}

	out, err = executor.Host.Exec(executor.ContainerRuntime, "network", "create", vrfNextHopSettings.multiHopNetwork, "--ipv6",
		"--driver=bridge", "--subnet=172.32.0.0/16", "--subnet=fc00:f853:ccd:e800::/64")
	if err != nil && !strings.Contains(out, "already exists") {
		return nil, errors.Join(err, fmt.Errorf("failed to create %s: %s", vrfNextHopSettings.multiHopNetwork, out))
	}

	config := vrfContainersConfig(image)

	vrfContainers, err := frrcontainer.Create(config)
	if err != nil {
		return nil, err
	}

	err = vrfSetup(cs)
	if err != nil {
		return nil, err
	}
	err = multiHopSetUp(vrfContainers, vrfNextHopSettings, cs)
	if err != nil {
		return nil, err
	}

	return vrfContainers, nil
}

// InfraTearDown tears down the containers and the routes needed for bgp testing.
func InfraTearDown(cs *clientset.Clientset) error {
	return infraTearDown(cs, FRRContainers, defaultNextHopSettings, func(c *frrcontainer.FRR) bool {
		return !isVRFContainer(c)
	})
}

// InfraTearDown tears down the containers and the routes needed for bgp testing.
func InfraTearDownVRF(cs *clientset.Clientset) error {
	return infraTearDown(cs, FRRContainers, vrfNextHopSettings, isVRFContainer)
}

func infraTearDown(cs *clientset.Clientset, containers []*frrcontainer.FRR, nextHop nextHopSettings, filter func(*frrcontainer.FRR) bool) error {
	filtered := make([]*frrcontainer.FRR, 0)
	for _, c := range containers {
		if filter(c) {
			filtered = append(filtered, c)
		}
	}

	multiHopRoutes := map[string]container.NetworkSettings{}
	var err error
	if containsMultiHop(filtered) {
		multiHopRoutes, err = container.Networks(nextHop.nextHopContainerName)
		if err != nil {
			return err
		}
	}

	err = frrcontainer.Delete(filtered)
	if err != nil {
		return err
	}

	if len(multiHopRoutes) == 0 {
		return nil
	}

	err = multiHopTearDown(nextHop, multiHopRoutes, cs)
	if err != nil {
		return err
	}

	return nil
}

// multiHopSetUp prepares a multihop scenario taking nextHop settings, which include:
// - a container that acts as next hop.
// - the docker network the nodes are connected to (and expected to perform the next hop peering).
// - an additional docker network connected both do the "next hop" container and to the remote container, acting as last hop.
// - an optional routing table to inject the routes to, useful in case the interface belongs to a vrf.
func multiHopSetUp(containers []*frrcontainer.FRR, nextHop nextHopSettings, cs *clientset.Clientset) error {
	err := addContainerToNetwork(nextHop.nextHopContainerName, nextHop.multiHopNetwork)
	if err != nil {
		return errors.Join(err, fmt.Errorf("failed to connect %s to %s", nextHop.nextHopContainerName, nextHop.multiHopNetwork))
	}

	multiHopRoutes, err := container.Networks(nextHop.nextHopContainerName)
	if err != nil {
		return err
	}

	for _, c := range containers {
		if c.Network == nextHop.multiHopNetwork {
			err = container.AddMultiHop(c, c.Network, nextHop.nodeNetwork, defaultRoutingTable, multiHopRoutes)
			if err != nil {
				return errors.Join(err, fmt.Errorf("failed to set up the multi-hop network for container %s", c.Name))
			}
		}
	}
	err = addMultiHopToNodes(cs, nextHop.nodeNetwork, nextHop.multiHopNetwork, nextHop.nodeRoutingTable, multiHopRoutes)
	if err != nil {
		return errors.Join(err, errors.New("failed to set up the multi-hop network"))
	}

	return nil
}

func vrfSetup(cs *clientset.Clientset) error {
	nodes, err := cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, node := range nodes.Items {
		err := addContainerToNetwork(node.Name, vrfNetwork)
		if err != nil {
			return errors.Join(err, fmt.Errorf("failed to connect %s to %s", node.Name, vrfNetwork))
		}

		err = container.SetupVRFForNetwork(node.Name, vrfNetwork, VRFName, vrfNextHopSettings.nodeRoutingTable)
		if err != nil {
			return err
		}
	}
	return nil
}

func externalContainersConfigs() map[string]frrcontainer.Config {
	res := make(map[string]frrcontainer.Config)
	res["ibgp-single-hop"] = frrcontainer.Config{
		Name: "ibgp-single-hop",
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			Password: "ibgp-test",
			MultiHop: false,
		},
		Router: frrconfig.RouterConfig{
			ASN:      FRRK8sASN,
			BGPPort:  179,
			Password: "ibgp-test",
		},
	}
	res["ibgp-multi-hop"] = frrcontainer.Config{
		Name: "ibgp-multi-hop",
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			Password: "ibgp-test",
			MultiHop: true,
		},
		Router: frrconfig.RouterConfig{
			ASN:      FRRK8sASN,
			BGPPort:  180,
			Password: "ibgp-test",
		},
	}
	res["ebgp-multi-hop"] = frrcontainer.Config{
		Name: "ebgp-multi-hop",
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			Password: "ebgp-test",
			MultiHop: true,
		},
		Router: frrconfig.RouterConfig{
			ASN:      externalASN,
			BGPPort:  180,
			Password: "ebgp-test",
		},
	}
	res["ebgp-single-hop"] = frrcontainer.Config{
		Name: "ebgp-single-hop",
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			MultiHop: false,
		},
		Router: frrconfig.RouterConfig{
			ASN:     externalASN,
			BGPPort: 179,
		},
	}
	return res
}

func hostnetContainerConfig(image string) map[string]frrcontainer.Config {
	res := make(map[string]frrcontainer.Config)
	res["ibgp-single-hop"] = frrcontainer.Config{
		Name:  "ibgp-single-hop",
		Image: image,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			Password: "ibgp-test",
			MultiHop: false,
		},
		Router: frrconfig.RouterConfig{
			ASN:      FRRK8sASN,
			BGPPort:  179,
			Password: "ibgp-test",
		},
		Network:  "host",
		HostIPv4: hostIPv4,
		HostIPv6: hostIPv6,
	}
	return res
}

func frrContainersConfigs(image string) map[string]frrcontainer.Config {
	res := make(map[string]frrcontainer.Config)
	res["ibgp-single-hop"] = frrcontainer.Config{
		Name:  "ibgp-single-hop",
		Image: image,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			Password: "ibgp-test",
			MultiHop: false,
		},
		Router: frrconfig.RouterConfig{
			ASN:      FRRK8sASN,
			BGPPort:  179,
			Password: "ibgp-test",
		},
		Network: kindNetwork,
	}

	res["ibgp-multi-hop"] = frrcontainer.Config{
		Name:  "ibgp-multi-hop",
		Image: image,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			Password: "ibgp-test",
			MultiHop: true,
		},
		Router: frrconfig.RouterConfig{
			ASN:      FRRK8sASN,
			BGPPort:  180,
			Password: "ibgp-test",
		},
		Network:  defaultNextHopSettings.multiHopNetwork,
		HostIPv4: hostIPv4,
		HostIPv6: hostIPv6,
	}
	res["ebgp-multi-hop"] = frrcontainer.Config{
		Name:  "ebgp-multi-hop",
		Image: image,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			Password: "ebgp-test",
			MultiHop: true,
		},
		Router: frrconfig.RouterConfig{
			ASN:      externalASN,
			BGPPort:  180,
			Password: "ebgp-test",
		},
		Network:  defaultNextHopSettings.multiHopNetwork,
		HostIPv4: hostIPv4,
		HostIPv6: hostIPv6,
	}
	res["ebgp-single-hop"] = frrcontainer.Config{
		Name:    "ebgp-single-hop",
		Image:   image,
		Network: kindNetwork,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASN,
			MultiHop: false,
		},
		Router: frrconfig.RouterConfig{
			ASN:     externalASN,
			BGPPort: 179,
		},
	}
	return res
}

func vrfContainersConfig(image string) map[string]frrcontainer.Config {
	res := make(map[string]frrcontainer.Config)
	res["ebgp-vrf-single-hop"] = frrcontainer.Config{
		Name:    "ebgp-vrf-single-hop",
		Image:   image,
		Network: vrfNetwork,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASNVRF,
			Password: "vrf-test",
			MultiHop: false,
		},
		Router: frrconfig.RouterConfig{
			ASN:      externalASN,
			Password: "vrf-test",
			BGPPort:  179,
			VRF:      VRFName,
		},
	}
	res["ibgp-vrf-single-hop"] = frrcontainer.Config{
		Name:    "ibgp-vrf-single-hop",
		Image:   image,
		Network: vrfNetwork,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASNVRF,
			Password: "vrf-test",
			MultiHop: false,
		},
		Router: frrconfig.RouterConfig{
			ASN:      FRRK8sASNVRF,
			BGPPort:  179,
			Password: "vrf-test",
			VRF:      VRFName,
		},
	}
	res["ibgp-vrf-multi-hop"] = frrcontainer.Config{
		Name:  "ibgp-vrf-multi-hop",
		Image: image,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASNVRF,
			Password: "ibgp-test",
			MultiHop: true,
		},
		Router: frrconfig.RouterConfig{
			ASN:      FRRK8sASNVRF,
			BGPPort:  180,
			Password: "ibgp-test",
			VRF:      VRFName,
		},
		Network: vrfNextHopSettings.multiHopNetwork,
	}
	res["ebgp-vrf-multi-hop"] = frrcontainer.Config{
		Name:  "ebgp-vrf-multi-hop",
		Image: image,
		Neighbor: frrconfig.NeighborConfig{
			ASN:      FRRK8sASNVRF,
			MultiHop: true,
		},
		Router: frrconfig.RouterConfig{
			ASN:     externalASN,
			BGPPort: 180,
			VRF:     VRFName,
		},
		Network: vrfNextHopSettings.multiHopNetwork,
	}

	return res
}

func multiHopTearDown(nextHop nextHopSettings, routes map[string]container.NetworkSettings, cs *clientset.Clientset) error {
	out, err := executor.Host.Exec(executor.ContainerRuntime, "network", "rm", nextHop.multiHopNetwork)
	if err != nil {
		return errors.Join(err, fmt.Errorf("failed to remove %s: %s", nextHop.multiHopNetwork, out))
	}

	nodes, err := cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, node := range nodes.Items {
		nodeExec := executor.ForContainer(node.Name)
		err = container.DeleteMultiHop(nodeExec, nextHop.nodeNetwork, nextHop.multiHopNetwork, nextHop.nodeRoutingTable, routes)
		if err != nil {
			return errors.Join(err, fmt.Errorf("failed to delete multihop routes for pod %s", node.Name))
		}
	}

	return nil
}

// Allow the cluster nodes to reach the multi-hop network containers.
func addMultiHopToNodes(cs *clientset.Clientset, targetNetwork, multiHopNetwork string, routingtable string, multiHopRoutes map[string]container.NetworkSettings) error {
	/*
		When "host" network is not specified we assume that the tests
		run on a kind cluster, where all the nodes are actually containers
		on our pc. This allows us to create containerExecutors for the cluster
		nodes, and edit their routes without any added privileges.
	*/
	nodes, err := cs.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, node := range nodes.Items {
		nodeExec := executor.ForContainer(node.Name)
		err := container.AddMultiHop(nodeExec, targetNetwork, multiHopNetwork, routingtable, multiHopRoutes)
		if err != nil {
			return err
		}
	}
	return nil
}

// validateContainersNames validates that the given string is a comma separated list of containers names.
// The valid names are: ibgp-single-hop / ibgp-multi-hop / ebgp-single-hop / ebgp-multi-hop.
func validateContainersNames(containerNames string) error {
	if len(containerNames) == 0 {
		return fmt.Errorf("failed to validate containers names: got empty string")
	}
	validNames := map[string]bool{
		"ibgp-single-hop": true,
		"ibgp-multi-hop":  true,
		"ebgp-single-hop": true,
		"ebgp-multi-hop":  true,
	}
	names := strings.Split(containerNames, ",")
	for _, n := range names {
		v, ok := validNames[n]
		if !ok {
			return fmt.Errorf("failed to validate container name: %s invalid name", n)
		}
		if !v {
			return fmt.Errorf("failed to validate container name: %s duplicate name", n)
		}
		validNames[n] = false
	}

	return nil
}

// containsMultiHop returns true if the given containers list include a multi-hop container.
func containsMultiHop(frrContainers []*frrcontainer.FRR) bool {
	var multiHop = false
	for _, frr := range frrContainers {
		if strings.Contains(frr.Name, "multi-hop") {
			multiHop = true
		}
	}

	return multiHop
}

func addContainerToNetwork(containerName, network string) error {
	networks, err := container.Networks(containerName)
	if err != nil {
		return err
	}
	if _, ok := networks[network]; ok {
		return nil
	}

	out, err := executor.Host.Exec(executor.ContainerRuntime, "network", "connect",
		network, containerName)
	if err != nil && !strings.Contains(out, "already exists") {
		return nil
	}
	if err != nil {
		return errors.Join(err, fmt.Errorf("failed to connect %s to %s: %s", containerName, network, out))
	}
	return nil
}

func isVRFContainer(c *frrcontainer.FRR) bool {
	return strings.Contains(c.Name, "vrf")
}
