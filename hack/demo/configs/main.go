// SPDX-License-Identifier:Apache-2.0

package main

import (
	"flag"
	"html/template"
	"os"
)

type BGPD struct {
	FrrIPv4    string
	FrrIPv6    string
	SsVRF      string
	SsFrrIPv4  string
	SsFrrIPv6  string
}

func main() {
	frrIPv4 := flag.String("frr-ipv4", "", "frr ipv4")
	frrIPv6 := flag.String("frr-ipv6", "", "frr ipv6")
	ssVRF := flag.String("ss-vrf", "", "extra session vrf")
	ssFrrIPv4 := flag.String("ss-frr-ipv4", "", "extra session frr ipv4")
	ssFrrIPv6 := flag.String("ss-frr-ipv6", "", "extra session frr ipv6")
	flag.Parse()
	data := BGPD{
		FrrIPv4:   *frrIPv4,
		FrrIPv6:   *frrIPv6,
		SsVRF:     *ssVRF,
		SsFrrIPv4: *ssFrrIPv4,
		SsFrrIPv6: *ssFrrIPv6,
	}

	t, err := template.New("receive_all.yaml.tmpl").ParseFiles("receive_all.yaml.tmpl")
	if err != nil {
		panic(err)
	}
	f, err := os.Create("receive_all.yaml")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	err = t.Execute(f, data)
	if err != nil {
		panic(err)
	}
}