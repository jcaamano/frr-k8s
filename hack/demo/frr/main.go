// SPDX-License-Identifier:Apache-2.0

package main

import (
	"flag"
	"fmt"
	"html/template"
	"os"
	"strings"
)

type BGPD struct {
	FrrIP    string
	NodesIP  []string
	Protocol string
}

func main() {
	nodeList := flag.String("nodes", "", "nodes ip")
	frrIP := flag.String("frr", "", "frr ip")
	flag.Parse()
	fmt.Println(*frrIP)
	fmt.Println(*nodeList)
	data := BGPD{
		FrrIP: *frrIP,
		NodesIP: strings.Split(*nodeList, " "),
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
