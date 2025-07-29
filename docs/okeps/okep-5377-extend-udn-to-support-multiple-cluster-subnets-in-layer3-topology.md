# OKEP-5377: Extend UDN to Support Multiple Cluster Subnets in Layer3 Topology

* Issue: [#5377](https://github.com/ovn-org/ovn-kubernetes/issues/5377)

## Problem Statement

UDN currently supports only one cluster subnet per IP family in Layer3 topology. This limits IP
address planning and scalability for large clusters.

## Goals

* Support multiple cluster subnets per IP family in UDN's Layer3 topology.
* Align UDN’s subnet capabilities with existing support in NAD.
* Allow dynamic subnet expansion by appending new subnets to an existing UDN.
* Support only subnets with the same host subnet size(`hostSubnet`).

## Non-Goals

* Supporting multiple subnets in Layer2 or Localnet topologies.
* Supporting deletion or shrinking of existing subnets.

## Future-Goals

* Support subnets with different host subnet sizes(`hostSubnet`).
  This feaure requires a OVN commit [27cc274](https://github.com/ovn-org/ovn/commit/27cc274e66acd9e0ed13525f9ea2597804107348).

## Introduction

When using UDN to define a network in Layer3 topology, each node gets a subnet allocated from the
UDN's `subnets` list per IP family. Currently, UDN limits `subnets` to a single subnet per IP
family, enforced by CRD validation and full immutability of the UDN spec. This limitation blocks
practical use cases where operators need to expand IP space incrementally — a feature already
available through NAD.

This proposal aims to bring UDN's flexibility in line with NAD by supporting multiple cluster
subnets and enabling controlled mutability.


## User-Stories/Use-Cases

### Story 1: Expanding subnet pool for growing clusters

**As a** cluster operator managing a UDN Layer3 network,
**I want** to add new subnets when existing ones near exhaustion,
**so that** I can scale the cluster without recreating the UDN object.


## Proposed Solution

### API Details

1. Update `UDN`/`CUDN` CRD to allow `layer3.subnets` to accept multiple subnets per IP family.
2. Allow updates to the `layer3.subnets` field, but only to **append** new entries.
   - Reject updates that attempt to remove or modify existing subnet entries.
   - Reject new entries that overlap with existing subnets or use a different `hostSubnet`.


### Implementation Details

### Node Subnet Allocation

The current NAD implementation already supports multiple subnets in Layer3 topology. However,
when a new subnet is added to a NAD, the corresponding network controller is first deleted and then
recreated with the updated subnet list. This behavior can cause node subnets to be reallocated,
resulting in unnecessary disruption.

In practice, this issue can often be worked around by restarting the `ovnkube-cluster-manager`, so
that the controller is recreated with the latest NAD spec and reserve node subnets based on the
`k8s.ovn.org/node-subnets` annotation.

Ideally, nodes should retain their originally assigned subnets when adding new cluster subnets.
Changes are required in the `SecondaryNetworkClusterManager` to ensure stable node subnet assignments
when new subnets are dynamically added.

### OVN Network Topology

Adding new subnets to a Layer3 network simply extends its cluster subnet allocation pool. Each node
continues to allocate one subnet from the available pool. The overall OVN network topology remains
largely unchanged. However, one additional change is required in the routing policies of the OVN
Cluster Router: the `DefaultNoReroutePriority` (priority 102) policies.

Currently it only allows east-west traffic within the individual cluster subnet.

For example, for a network with cluster subnets `10.1.0.0/16` and `10.2.0.0/16`, the OVN cluster
router policy list includes:
```
$ ovn-nbctl lr-policy-list  udn_udn.primary.layer3_ovn_cluster_router
Routing Policies
      ...
      102   ip4.src == 10.1.0.0/16 && ip4.dst == 10.1.0.0/16    allow
      102   ip4.src == 10.2.0.0/16 && ip4.dst == 10.2.0.0/16    allow
```

To enable east-west traffic between the subnets, the following additional policies must be added:
```
$ ovn-nbctl lr-policy-list  udn_udn.primary.layer3_ovn_cluster_router
Routing Policies
      ...
      102   ip4.src == 10.1.0.0/16 && ip4.dst == 10.1.0.0/16    allow
      102   ip4.src == 10.1.0.0/16 && ip4.dst == 10.2.0.0/16    allow
      102   ip4.src == 10.2.0.0/16 && ip4.dst == 10.2.0.0/16    allow
      102   ip4.src == 10.2.0.0/16 && ip4.dst == 10.1.0.0/16    allow
```


### API Validations

The `subnets` field in the current CRD has the following validation rules:
```
	// +kubebuilder:validation:MaxItems=2
	// +kubebuilder:validation:XValidation:rule="size(self) != 2 || !isCIDR(self[0].cidr) || !isCIDR(self[1].cidr) || cidr(self[0].cidr).ip().family() != cidr(self[1].cidr).ip().family()", message="When 2 CIDRs are set, they must be from different IP families"
	Subnets []Layer3Subnet `json:"subnets,omitempty"`
```

* `MaxItems` is required to keep CEL evaluation costs below threshold. It has to be
  retained but adjusted to a reasonable value (the max value is 400 for the new rules defined below).
* The `XValidation:rule` must be removed to allow more than one subnet per IP family.

New validation logic should be added to enforce the following constraints on `layer3.subnets`:
* Prevent removal or modification of existing subnets:
  ```yaml
    - message: Removing existing subnets is not allowed
      rule: '!has(oldSelf.subnets) || oldSelf.subnets.all(old, self.subnets.exists(new,
        new.cidr == old.cidr))'
  ```
* Prevent overlapping or nested subnets:
  ```yaml
    - message: Subnets must not overlap or contain each other
      rule: '!has(self.subnets) || self.subnets.size() == 1 || !self.subnets.exists(i,
        self.subnets.exists(j, i != j && cidr(i.cidr).containsCIDR(j.cidr)))'
  ```
* Ensure all subnets use the same hostSubnet size:
  ```yaml
    - message: All subnets must use the same hostSubnet value
      rule: '!has(self.subnets) || self.subnets.size() == 1 || self.subnets.all(i,
        i.hostSubnet == self.subnets[0].hostSubnet)'
  ```

Additionally, the validation rule that makes the entire UDN `spec` immutable must be removed:
```
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="Spec is immutable"
	Spec UserDefinedNetworkSpec `json:"spec"`
```
Instead, immutability should be enforced at the sub-field level to preserve immutability where
needed.


### Testing Details

* **Unit Tests:** Extend subnet allocator tests to cover multiple subnets and dynamic expansion scenarios.
* **E2E Tests:** Test UDN behavior when new subnets are appended, and verify that nodes receive
  allocations from the correct ranges.
* **API Tests:** Validate CRD updates for append-only behavior and ensure invalid changes (e.g., removal
  or modification of existing subnets) are correctly rejected.


### Documentation Details

* Update `mkdocs.yml` with the OKEP reference.

## Risks, Known Limitations and Mitigations

### Limitations

1. **Subnets are append-only**: Removing or modifying existing subnets after creation is not
   supported.

2. **Kubernetes version requirement**: The new CRD validation rules use `containsCIDR()`, which
   requires Kubernetes 1.21 or newer.


3. **Uniform `hostSubnet` size**: All subnets must use the same `hostSubnet` size.
  Supporting subnets with different `hostSubnet` sizes is currently downstream-only and depends on
  OVN commit [27cc274](https://github.com/ovn-org/ovn/commit/27cc274e66acd9e0ed13525f9ea2597804107348)
  *northd: Use lower priority for all src routes*. This limitation may be lifted in the future.

4. **Missing routes in existing Pods for new subnet (Secondary Layer3 networks only)**:
   When a new subnet is added to a secondary layer3 network, the existing Pods don't have the route
   for reaching the IPs in the new subnet, so the traffics targeting the new subnet will go via primary
   interface. For example, create UDN with one cluster subnet:

   When a new subnet is added to a secondary Layer3 network, existing Pods do not receive updated
   routes to reach the new subnet. As a result, traffic to IPs in the new subnet is routed via the
   primary network interface. The issue is depicted as below:

   - A Secondary Layer3 network is created with one cluster subnet:
      ```yaml
      apiVersion: k8s.ovn.org/v1
      kind: UserDefinedNetwork
      metadata:
         name: udn-secondary-layer3
      spec:
         topology: Layer3
         layer3:
            role: Secondary
            subnets:
            - cidr: 10.10.0.0/16
            hostSubnet: 17
      ```
   - Node A allocates a subnet from `10.10.0.0/16`:
      ```bash
         k8s.ovn.org/node-subnets: '{...,"udn_udn-secondary-layer3":["10.10.128.0/17"]}'
      ```
   - Pod A is created on Node A and gets the following secondary network config:
      ```
      k8s.ovn.org/pod-networks: '{
         ...,
         "udn/udn-secondary-layer3":{
            "ip_addresses":["10.10.128.4/17"],"mac_address":"0a:58:0a:0a:80:04",
            "routes":[
                  {"dest":"10.10.0.0/16","nextHop":"10.10.128.1"}],
            "ip_address":"10.10.128.4/17","role":"secondary"}}'
      ```
   - A new subnet is added to the UDN:
      ```
      apiVersion: k8s.ovn.org/v1
      kind: UserDefinedNetwork
      metadata:
      name: udn-secondary-layer3
      namespace: udn
      spec:
      layer3:
         role: Secondary
         subnets:
         - cidr: 10.10.0.0/16
            hostSubnet: 17
         - cidr: 10.20.0.0/16
            hostSubnet: 17
      topology: Layer3
      ```
   - Node B allocates a subnet from `10.20.0.0/16`:
      ```
      k8s.ovn.org/node-subnets: '{"default":["10.192.2.0/24"],"udn_udn-primary-layer3":["10.2.0.0/17"],"udn_udn-secondary-layer3":["10.20.0.0/17"]}'
      ```
   - Pod B is created on Node B and receives both route entries:
      ```
      k8s.ovn.org/pod-networks: '{
         ...,
         "udn/udn-secondary-layer3":{
            "ip_addresses":["10.20.0.4/17"],"mac_address":"0a:58:0a:14:00:04",
            "routes":[
                  {"dest":"10.10.0.0/16","nextHop":"10.20.0.1"},
                  {"dest":"10.20.0.0/16","nextHop":"10.20.0.1"}],
            "ip_address":"10.20.0.4/17","role":"secondary"}}'
      ```
   - traffic from Pod A to Pod B is routed via the primary network:
      ```bash
      root@client-a:/# ip route get 10.20.0.4
      10.20.0.4 via 10.1.0.1 dev ovn-udn1 src 10.1.0.10 uid 0
      cache
      ```

   This issue occurs because the existing Pod (Pod A) lacks route updates to the newly added
   subnet. It can be resolved by recreating affected Pods.


   **Note**: This issue does not affect the primary network, as its interface is always the default
   gateway:
   ```
   root@client-a:/# ip route
   default via 10.1.0.1 dev ovn-udn1
   ...
   ```

## OVN Kubernetes Version Skew

To be updated based on reviewer feedback.

## Alternatives

* Recreate UDN with new subnet list — disruptive and impractical for large clusters.
* Use NAD for Layer3 — not applicable for users who standardize on UDN.

