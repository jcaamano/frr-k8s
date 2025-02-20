#!/bin/bash
set -x

function docker_get_br_net_by_subnet() {
    docker network ls -f 'driver=bridge' -q | xargs docker network inspect | jq -r 'try .[] | select(any(.IPAM.Config[]; .Subnet=="'"$1"'")) | .Name'
}

function podman_get_br_net_by_subnet() {
    podman network ls -f 'driver=bridge' -q | xargs podman network inspect | jq -r 'try .[] | select(any(.subnets[]; .subnet=="'"$1"'")) | .name'
}

CLI=docker
CLI_BR_NET_BY_SUBNET_FN="docker_get_br_net_by_subnet"
if ! command -v $CLI; then
    CLI=podman
    CLI_BR_NET_BY_SUBNET_FN="podman_get_br_net_by_subnet"
fi

echo "CLI is: $CLI"

# A second (BGP) session (SS) can be configured over a vlan with extra
# positional arguments, respectively:
# - The controller interface for the vlan
# - The vlan id
# - The VRF for the session
# - A sed expression to apply to the main BGP session IPs resulting in the
#   second BGP session IPs
SS_IFACE=${1}
SS_VRF=${2}
SS_VLAN=${3}
SS_SED_V4=${4:-'s|111|221|g'}
SS_SED_V6=${5:-'s|c956|ca56|g'}

SS_VLAN=${SS_VLAN:-221}
SS_VRF_REMOTE=${SS_VRF}
SS_VRF_LOCAL=${SS_VRF}-ext

NODE_IPS_V4=$(kubectl get nodes -o jsonpath='{.items[*].status.addresses[?(@.type=="InternalIP")].address}' | grep -Po '(?<=\s|^)[0-9.]+' | tr '\n' ' ')
NODE_IPS_V6=$(kubectl get nodes -o jsonpath='{.items[*].status.addresses[?(@.type=="InternalIP")].address}' | grep -Po '(?<=\s|^)[0-9a-f]+:[0-9a-f:]+' | tr '\n' ' ')

GW_IP=""
NETWORK=""
PREFIX=""
function getNodeGatewayAndNetwork() {
    IP_CMD=$1
    NODE=$2
    GW_IFACE=$($IP_CMD -j -d route get $NODE | jq -r '.[] | .dev')
    GW_IP=$($IP_CMD -d -j address show $GW_IFACE | jq -r '.[] | .addr_info[0].local')
    PREFIX=$($IP_CMD -d -j address show $GW_IFACE | jq -r '.[] | .addr_info[0].prefixlen')
    SUBNET=$($IP_CMD -j -d route get fibmatch $NODE | jq -r '.[] | .dst')
    NETWORK=$(eval $CLI_BR_NET_BY_SUBNET_FN $SUBNET)
    if [ -z "$NETWORK" ]; then
        # assume libvirt
	    NETWORK=host
    fi
}

for node in $NODE_IPS_V4; do
    getNodeGatewayAndNetwork "ip" "$node"
    GW_IP_V4=$GW_IP
    PREFIX_V4=$PREFIX
    break
done
for node in $NODE_IPS_V6; do
    getNodeGatewayAndNetwork "ip -6" "$node" 
    GW_IP_V6=$GW_IP
    PREFIX_V6=$PREFIX
    break
done

FRR_CONF_ARGS+=(-nodes-ipv4 "$NODE_IPS_V4")
FRR_CONF_ARGS+=(-nodes-ipv6 "$NODE_IPS_V6")

if [ -n "$SS_IFACE" ]; then
    [ "$NETWORK" != "host" ] && echo "EXTRA_NETWORK only supported in host network" && exit 1
    SS_FRR_IP_V4=$(echo ${GW_IP_V4} | sed ${SS_SED_V4})
    SS_FRR_IP_V6=$(echo ${GW_IP_V6} | sed ${SS_SED_V6})
    SS_NODE_IPS_V4=$(echo ${NODE_IPS_V4} | sed ${SS_SED_V4})
    SS_NODE_IPS_V6=$(echo ${NODE_IPS_V6} | sed ${SS_SED_V6})
    FRR_CONF_ARGS+=(-ss-vrf "${SS_VRF_LOCAL}")
    FRR_CONF_ARGS+=(-ss-nodes-ipv4 "$SS_NODE_IPS_V4")
    FRR_CONF_ARGS+=(-ss-nodes-ipv6 "$SS_NODE_IPS_V6")

    MAYBE_SUDO=
    if (( $EUID != 0 )); then
        echo "Not running as root, will require passwordless sudo"
        MAYBE_SUDO="sudo -n"
    fi

    $MAYBE_SUDO ip link add ${SS_VRF_LOCAL} type vrf table ${SS_VLAN} && {
        $MAYBE_SUDO  ip link set dev ${SS_VRF_LOCAL} up
    } || true
    $MAYBE_SUDO  ip link add link ${SS_IFACE} name ${SS_IFACE}.${SS_VLAN} type vlan id ${SS_VLAN} && {
        $MAYBE_SUDO  ip link set dev ${SS_IFACE}.${SS_VLAN} master ${SS_VRF_LOCAL}
        $MAYBE_SUDO  ip address add dev ${SS_IFACE}.${SS_VLAN} ${SS_FRR_IP_V4}/${PREFIX_V4}
        $MAYBE_SUDO  ip link set dev ${SS_IFACE}.${SS_VLAN} up
	[ -n "$SS_FRR_IP_V6" ] && $MAYBE_SUDO  ip -6 address add dev ${SS_IFACE}.${SS_VLAN} ${SS_FRR_IP_V6}/${PREFIX_V6}
    } || true
fi

pushd ./frr/ && {
  go run . "${FRR_CONF_ARGS[@]}"
} && popd || exit 1

FRR_CONFIG=$(mktemp -d -t frr-XXXXXXXXXX)
cp frr/*.conf $FRR_CONFIG
cp frr/daemons $FRR_CONFIG
chmod a+rwx -R $FRR_CONFIG

$CLI rm -f frr
$CLI run -d --privileged --network $NETWORK --rm --ulimit core=-1 --name frr --volume "$FRR_CONFIG":/etc/frr quay.io/frrouting/frr:10.4.1

if [ "$NETWORK" != "host" ]; then
    FRR_IP_V4=$($CLI inspect -f "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}" frr)
    FRR_IP_V6=$($CLI inspect -f "{{range .NetworkSettings.Networks}}{{.GlobalIPv6Address}}{{end}}" frr)
else
    FRR_IP_V4=$GW_IP_V4
    FRR_IP_V6=$GW_IP_V6
fi
for i in configs/*.yaml; do
    rm $i
done

cp configs/templates/*.yaml configs/

FRR_IP=$FRR_IP_V4
if [ -z "$FRR_IP" ]; then
    FRR_IP=$FRR_IP_V6
fi

for i in configs/*.yaml; do
    sed -i "s/NEIGHBOR_IP/$FRR_IP/g" $i
done

K8S_CONF_ARGS+=(-frr-ipv4 "$FRR_IP_V4")
K8S_CONF_ARGS+=(-frr-ipv6 "$FRR_IP_V6")
if [ -n "$SS_IFACE" ]; then
    K8S_CONF_ARGS+=(-ss-vrf "${SS_VRF_REMOTE}")
    K8S_CONF_ARGS+=(-ss-frr-ipv4 "$SS_FRR_IP_V4")
    K8S_CONF_ARGS+=(-ss-frr-ipv6 "$SS_FRR_IP_V6")
fi

pushd ./configs && {
    go run . "${K8S_CONF_ARGS[@]}"
} && popd || exit 1

echo "NETWORK is: $NETWORK"
echo "FRR IP V4 is: $FRR_IP_V4"
echo "FRR IP V6 is: $FRR_IP_V6"
echo "Nodes IPs V4 are: $NODE_IPS_V4"
echo "Nodes IPs V6 are: $NODE_IPS_V6"
echo "Second session FRR IP V4 is: $SS_FRR_IP_V4"
echo "Second session FRR IP V6 is: $SS_FRR_IP_V6"
echo "Extra network Nodes IPs V4 are: $SS_NODE_IPS_V4"
echo "Extra network Nodes IPs V6 are: $SS_NODE_IPS_V6"
echo "Setup is complete, demo yamls can be found in $(pwd)/configs"

