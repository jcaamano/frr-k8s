// SPDX-License-Identifier:Apache-2.0

package main

import (
	"flag"
	"html/template"
	"os"
	"strings"
)

type BGPD struct {
	NodesIPv4    []string
	NodesIPv6    []string
	SsVRF        string
	SsNodesIPv4  []string
	SsNodesIPv6  []string
}

func split(s, sep string) []string {
	if len(s) == 0 {
		return nil
	}
	return strings.Split(s, sep)
}

func main() {
	nodesIPv4 := flag.String("nodes-ipv4", "", "nodes ipv4")
	nodesIPv6 := flag.String("nodes-ipv6", "", "nodes ipv6")
	ssVRF := flag.String("ss-vrf", "", "second session vrf")
	ssNodesIPv4 := flag.String("ss-nodes-ipv4", "", "second session nodes ipv4")
	ssNodesIPv6 := flag.String("ss-nodes-ipv6", "", "second session nodes ipv6")
	flag.Parse()
	data := BGPD{
		NodesIPv4:   split(strings.Trim(*nodesIPv4, " "), " "),
		NodesIPv6:   split(strings.Trim(*nodesIPv6, " "), " "),
		SsVRF:       *ssVRF,
		SsNodesIPv4: split(strings.Trim(*ssNodesIPv4, " "), " "),
		SsNodesIPv6: split(strings.Trim(*ssNodesIPv6, " "), " "),
	}

	t, err := template.New("frr.conf.tmpl").ParseFiles("frr.conf.tmpl")
	if err != nil {
		panic(err)
	}
	f, err := os.Create("frr.conf")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	err = t.Execute(f, data)
	if err != nil {
		panic(err)
	}
}
