# METADATA
# title: "Compose services must not use host network mode"
# description: "Containers must not share the host network namespace."
# custom:
#   id: HF-COMPOSE-004
#   avd_id: HF-COMPOSE-004
#   severity: HIGH
#   input:
#     selector:
#       - type: yaml
package user.hookflow.compose_host_network

import rego.v1

deny contains res if {
	is_object(input.services)
	some name, svc in input.services
	svc.network_mode == "host"
	res := sprintf("Service '%s' must not use network_mode: host", [name])
}
