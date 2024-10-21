#!/bin/bash
set -x

CLI=docker
if ! command -v $CLI; then
    CLI=podman
fi

echo "CLI is: $CLI"

NODES=$(kubectl get nodes -o jsonpath={.items[*].status.addresses[?\(@.type==\"InternalIP\"\)].address})
echo "Nodes IPs are: $NODES"

for node in $NODES; do
    FRR_IP=$(ip -j -d route get $node | jq -r '.[] | .dev' | xargs ip -d -j address show | jq -r '.[] | .addr_info[0].local')
    NETWORK=$(docker network ls -f 'driver=bridge' -q | xargs docker network inspect | jq -r '.[] | select(any(.IPAM.Config[]; .Gateway=='"$gw"')) | .Name')
    if [ -n "$NETWORK" ]; then
        # assume container network
	FRR_IP=$(sudo docker inspect -f "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}" frr)
    else
        # assume libvirt
	NETWORK=host
    fi
    break
done

echo "FRR IP is: $FRR_IP"
echo "NETWORK is: $NETWORK"

pushd ./frr/
go run . -frr "$FRR_IP" -nodes "$NODES"
popd

FRR_CONFIG=$(mktemp -d -t frr-XXXXXXXXXX)
cp frr/*.conf $FRR_CONFIG
cp frr/daemons $FRR_CONFIG
chmod a+rwx -R $FRR_CONFIG

sudo docker rm -f frr
sudo docker run -d --privileged --network host --rm --ulimit core=-1 --name frr --volume "$FRR_CONFIG":/etc/frr quay.io/frrouting/frr:9.1.0

for i in configs/*.yaml; do
	rm $i
done

cp configs/templates/*.yaml configs/

for i in configs/*.yaml; do
    sed -i "s/NEIGHBOR_IP/$FRR_IP/g" $i
done

echo "Setup is complete, demo yamls can be found in $(pwd)/configs"
