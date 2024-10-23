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
    GW=$(ip -j -d route get $node | jq -r '.[] | .dev' | xargs ip -d -j address show | jq -r '.[] | .addr_info[0].local')
    NETWORK=$($CLI network ls -f 'driver=bridge' -q | xargs $CLI network inspect | jq -r 'try .[] | select(any(.IPAM.Config[]; .Gateway=="'"$GW"'")) | .Name')
    if [ -z "$NETWORK" ]; then
        # assume libvirt
	NETWORK=host
    fi
    break
done

pushd ./frr/
if [ "$NETWORK" = "host" ]; then
    FRR_IP=$GW
    go run . -frr "$FRR_IP" -nodes "$NODES"
else
    go run . -nodes "$NODES"
fi
popd

FRR_CONFIG=$(mktemp -d -t frr-XXXXXXXXXX)
cp frr/*.conf $FRR_CONFIG
cp frr/daemons $FRR_CONFIG
chmod a+rwx -R $FRR_CONFIG

sudo $CLI rm -f frr
sudo $CLI run -d --privileged --network host --rm --ulimit core=-1 --name frr --volume "$FRR_CONFIG":/etc/frr quay.io/frrouting/frr:9.1.0

if [ "$NETWORK" != "host" ]; then
    FRR_IP=$(sudo $CLI inspect -f "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}" frr)
fi

for i in configs/*.yaml; do
    rm $i
done

cp configs/templates/*.yaml configs/

for i in configs/*.yaml; do
    sed -i "s/NEIGHBOR_IP/$FRR_IP/g" $i
done

echo "FRR IP is: $FRR_IP"
echo "NETWORK is: $NETWORK"
echo "Setup is complete, demo yamls can be found in $(pwd)/configs"
