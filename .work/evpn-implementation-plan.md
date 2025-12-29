# Plan: Add EVPN Support to FRRConfiguration CRD

## Goal
Add support for EVPN configuration in the FRRConfiguration CRD, including:
- `advertise-all-vni` capability
- Route distinguisher (RD) configuration for VNIs
- Route target (RT) import/export configuration for L2 and L3 VNIs
- Mandatory distinction between L2 and L3 VNI types

## Confirmed Design Decisions
- **VNI Type Separation**: L2 and L3 VNIs are separate fields with distinct types
  - `L2VNIs []L2VNI` - array of L2 VNIs
  - `L3VNI *L3VNI` - optional singular L3 VNI (enforced by type system)
  - Type is implicit based on which field is used (no explicit Type field needed)
- **Struct Embedding**: VNIProperties contains common properties (RD, ImportRTs, ExportRTs)
  - L2VNI and L3VNI both have a direct VNI uint32 field and embed VNIProperties
  - AdvertisePrefixes field only exists on L3VNI
  - This avoids the `VNI.VNI` access pattern for the VNI number
- **Route Targets**: No "Both" field - only separate Import and Export arrays
- **Validation**:
  - RD format: ASN:NN or IP:NN
  - Import RT format: ASN:NN, IP:NN, or wildcard *:NN (for flexible import matching)
  - Export RT format: ASN:NN or IP:NN (wildcards NOT supported)
  - No support for "auto" keyword (if RTs are omitted, FRR handles automatically)
- **EVPN Scope**: Per-router configuration (can be on any VRF including default)
- **Architecture**: Both L2 and L3 VNIs can be configured on the same router
- **Neighbor Address Families**:
  - New optional field to control which address families a neighbor is activated for
  - Values: "unicast" (ipv4/ipv6 unicast based on neighbor IP) or "evpn" (l2vpn evpn)
  - Default: ["unicast"] (set via kubebuilder marker)
  - Can specify both: `addressFamilies: ["unicast", "evpn"]`
- **VNI Advertisement Logic**:
  - `AdvertiseVNIs` pointer controls VNI advertisement
  - Values: "Disabled" (no VNI advertisements), "All" (advertise all VNIs)
  - Default: nil (omitted)
  - **Scope**: Only valid for routers with EVPN neighbors (underlay routers)
  - Future: "Listed" (only advertise explicitly configured VNIs)

## Architecture Overview

The codebase follows this flow:
1. **API Types** (api/v1beta1/frrconfiguration_types.go) → User-facing Kubernetes API
2. **Translation Layer** (internal/controller/api_to_config.go) → Converts API types to internal FRR config structs
3. **Internal Types** (internal/frr/config.go) → Internal representation of FRR configuration
4. **Template Rendering** (internal/frr/templates/evpn.tmpl + frr.tmpl) → Go templates that generate actual FRR configuration

## Implementation Phases

### Phase 1: API Types (api/v1beta1/frrconfiguration_types.go) ✅ DONE

**Add to Router struct** (after line 90):
```go
// EVPN is the configuration for EVPN on this router.
// +optional
EVPN *EVPNConfig `json:"evpn,omitempty"`
```

**Add to Neighbor struct**:
```go
// AddressFamilies specifies which address families to activate this neighbor for.
// Supported values: "unicast" (IPv4/IPv6 unicast based on neighbor IP), "evpn" (L2VPN EVPN).
// Defaults to "unicast" when not specified.
// +optional
// +kubebuilder:default:=["unicast"]
// +kubebuilder:validation:MaxItems=2
// +kubebuilder:validation:Enum=unicast;evpn
AddressFamilies []string `json:"addressFamilies,omitempty"`
```

**Add new types**:

1. **EVPNConfig**: VNI advertisement and configuration
   - AdvertiseVNIs (*VNIAdvertisement, optional, default=nil) - controls L2 VNI auto-discovery
   - AdvertiseSVI (bool, optional) - enables advertising SVI IP/MAC as type-2 route
   - L2VNIs ([]L2VNI, optional) - array of L2 VNI customizations
   - L3VNI (*L3VNI, optional) - optional L3 VNI customization (can only be provided for routers with no neighbors)

2. **VNIAdvertisement**: Enum type
   - Constants: "Disabled", "All" (future: "Listed")

3. **VNIProperties**: Common properties for all VNI types (embedded struct)
   - RD (string, optional, pattern: `^([0-9]{1,10}|([0-9]{1,3}\.){3}[0-9]{1,3}):[0-9]{1,10}$`)
   - ImportRTs ([]ImportRouteTarget, optional, max 100 items)
   - ExportRTs ([]ExportRouteTarget, optional, max 100 items)

4. **L2VNI**: Layer 2 VNI configuration
   - VNI (uint32, required, range: 1-16777215)
   - VNIProperties (embedded with json:",inline")

5. **L3VNI**: Layer 3 VNI configuration
   - VNI (uint32, required, range: 1-16777215)
   - VNIProperties (embedded with json:",inline")
   - AdvertisePrefixes ([]string, **required**, enum: unicast) - controls prefix advertisement as EVPN type-5 routes
     - "unicast": auto-detects and advertises both ipv4 and ipv6 based on configured prefixes
     - Must contain exactly one value (currently only "unicast" is supported)
     - Future consideration: fine-grained control with separate ipv4/ipv6 values

6. **ImportRouteTarget**: Type alias with validation (for import route targets)
   - Pattern: Supports ASN:NN, IPv4:NN, or wildcard *:NN (where NN can be 16-bit or 32-bit)
   - Examples: "65000:1000", "192.0.2.1:1000", "*:100", "*:42000000"

7. **ExportRouteTarget**: Type alias with validation (for export route targets)
   - Pattern: Supports ASN:NN or IPv4:NN (wildcards NOT allowed)
   - Examples: "65000:1000", "192.0.2.1:1000"

### Phase 2: Internal Types, Translation & Merge ✅ DONE

This phase must come before webhook validation because the validation path
(`webhook → Validate() → apiToFRR() → mergeRouterConfigs()`) requires EVPN
configs to be translated and merged before they can be validated.

#### 2a: Internal EVPN Types (internal/frr/config.go)

**Add to RouterConfig struct**:
```go
EVPN *EVPNConfig
```

**Add new internal types**:
```go
type EVPNConfig struct {
    AdvertiseVNIs *string   // "Disabled" or "All"
    AdvertiseSVI  bool
    L2VNIs        []L2VNI
    L3VNI         *L3VNI
}

type VNIProperties struct {
    RD        string
    ImportRTs []string
    ExportRTs []string
}

type L2VNI struct {
    VNI uint32
    VNIProperties
}

type L3VNI struct {
    VNI uint32
    VNIProperties
    AdvertisePrefixes []string
}
```

#### 2b: API → Internal Translation (internal/controller/api_to_config.go)

**Add `evpnToFRR()` function** to convert `v1beta1.EVPNConfig → frr.EVPNConfig`.

**Modify `routerToFRRConfig()`** to wire in: `EVPN: evpnToFRR(r.EVPN)`.

#### 2c: EVPN Merge Logic (internal/controller/merge.go)

**Add `mergeEVPNConfigs()` function** called from `mergeRouterConfigs()`:
- If both nil → nil (no-op)
- If one nil → use the other
- If both non-nil:
  - `advertiseVNIs`: must be equal or error (non-mergeable)
  - `advertiseSVI`: must be equal or error (non-mergeable)
  - `l2vnis`: merge by VNI number:
    - Same VNI from two configs: RD must match (or one omitted), merge ImportRTs/ExportRTs
      (but if one omits RTs while the other specifies them → error)
    - Different VNIs: union
  - `l3vni`: must be equal or one nil or error (non-mergeable)

### Phase 3: Webhook Validation ✅ DONE (via post-merge validation in apiToFRR)

**Add validation functions** (all validations run after merging FRRConfiguration resources per node):

1. **validateEVPNConfig()**: Main EVPN validation
   - Check for duplicate VNI numbers across all routers (both L2 and L3)
   - Check that `advertiseVNIs`, `advertiseSVI`, and `l2vnis` are only configured on routers with EVPN neighbors
   - Check that `l3vni` is only configured on routers with no neighbors (temporary limitation)

2. **validateEVPNMergeConflicts()**: Validate non-mergeable field conflicts
   - Check for conflicting `advertiseVNIs` values for the same router across FRRConfigurations
   - Check for conflicting `advertiseSVI` values for the same router across FRRConfigurations
   - Check for conflicting RD values for the same VNI number across FRRConfigurations
   - Check for route target consistency: for the same VNI across FRRConfigurations, if one omits ImportRTs/ExportRTs (relying on FRR defaults) while another explicitly specifies them, the configuration is invalid

Note: Route targets (ImportRTs/ExportRTs) are mergeable when all configurations for the same VNI explicitly specify them - duplicates across merged configurations are removed during merge

**Integrate into existing validateConfig()** function

### Phase 4: Template Rendering ✅ DONE

**Create new template**: internal/frr/templates/evpn.tmpl
- Render `address-family l2vpn evpn` block
- Activate EVPN neighbors (those with "evpn" in AddressFamilies)
- If AdvertiseVNIs != nil && *AdvertiseVNIs == "All", render `advertise-all-vni`
- If AdvertiseSVI == true, render `advertise-svi-ip`
- Render L2 VNI blocks with RD and RTs inside `vni`/`exit-vni` (only when customized)
- Render L3 VNI RD and RTs at address-family level (no `vni` block)
- Render L3 VNI prefix advertisement (`advertise ipv4/ipv6 unicast`)
- Proper indentation and exit-address-family

**Modify frr.tmpl**:
- Add VRF-VNI binding blocks (`vrf X / vni Y / exit-vrf`) for L3VNIs
- Include `evpn` template after IPv6 prefixes block

**Modify neighboripfamily.tmpl**:
- Conditionally activate unicast based on `hasAddressFamily` check

**Expected FRR output (default VRF with L2VNIs)**:
```
router bgp 65000
  neighbor 192.0.2.10 remote-as 65001
  address-family ipv4 unicast
    neighbor 192.0.2.10 activate
  exit-address-family
  address-family l2vpn evpn
    neighbor 192.0.2.10 activate
    advertise-all-vni
    vni 1000
      rd 65000:1000
      route-target import 65000:1000
      route-target export 65000:1000
    exit-vni
  exit-address-family
```

**Expected FRR output (VRF router with L3VNI)**:
```
vrf red
  vni 2000
exit-vrf

router bgp 65000 vrf red
  address-family l2vpn evpn
    advertise ipv4 unicast
    advertise ipv6 unicast
    rd 65000:2000
    route-target import 65000:2000
    route-target export 65000:2000
  exit-address-family
```

Note: L2 VNI RD/RTs go inside a `vni`/`exit-vni` block. L3 VNI RD/RTs go at the address-family level (no `vni` block), matching FRR's IP-VRF configuration model.

### Phase 5: Testing ✅ DONE (unit + envtest; e2e pending)

1. **CRD Generation**: `make generate` and `make manifests` ✅
2. **Webhook Tests**: No EVPN-specific tests needed (webhook has no EVPN logic; validation is in apiToFRR) ✅
3. **Translation Tests**: 7 test cases in api_to_config_test.go (2 positive, 5 negative) ✅
4. **Template Tests**: 4 tests in frr_test.go with golden files ✅
5. **Merge Tests**: 16 test cases in merge_test.go ✅
6. **Envtest Controller Tests**: 2 specs (create + modify) in frrconfiguration_controller_test.go ✅
7. **Envtest CRD Validation Tests**: 13 specs (9 invalid, 4 valid) in frrconfiguration_crd_validation_test.go ✅
8. **API Docs**: Regenerated API-DOCS.md ✅

### Phase 6: Reference Host Configuration ✅ DONE

- `hack/evpn-setup.sh` — sets up VXLAN, bridge, VRF, and FRR EVPN config on nodes and external FRR container
- Used by Phase 7 e2e tests via `runEVPNSetupScript()` in `e2etests/tests/evpn.go`

### Phase 7: Integration Testing ✅ DONE

- `e2etests/tests/evpn.go` — L2 VNI and L3 VNI e2e tests
- L2 VNI test: validates BGP session, EVPN address family, VNI visibility, route exchange (JSON-based)
- L3 VNI test: validates BGP session, EVPN address family, type-5 routes with prefix→next-hop correlation (JSON-based)
- Both tests pass consecutively without manual intervention
- EVPN VRF name `evpnred` (avoids collision with infra VRF `red`)
- Tests validate control plane only (BGP route tables), no data plane traffic tests (consistent with pre-existing tests)

### Phase 8: Documentation ✅ DONE

- README.md updated with EVPN Configuration section (neighbor addressFamilies, advertiseVNIs, L2 VNI, L3 VNI examples)
- Design doc: `design/basic-evpn-support.md`
- API docs: `API-DOCS.md` (regenerated in Phase 5)
