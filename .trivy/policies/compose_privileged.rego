# METADATA
# title: "Compose services must not run privileged"
# description: "Containers must not set privileged to true."
# custom:
#   id: HF-COMPOSE-003
#   avd_id: HF-COMPOSE-003
#   severity: CRITICAL
#   input:
#     selector:
#       - type: yaml
package user.hookflow.compose_privileged

import rego.v1

deny contains res if {
	is_object(input.services)
	some name, svc in input.services
	svc.privileged == true
	res := sprintf("Service '%s' must not run with privileged: true", [name])
}
