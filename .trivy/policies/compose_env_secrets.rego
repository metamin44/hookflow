# METADATA
# title: "Compose environment secrets must use variable interpolation"
# description: "Secret-bearing environment variables in compose.yaml must reference environment variables rather than hardcoded literals."
# custom:
#   id: HF-COMPOSE-006
#   avd_id: HF-COMPOSE-006
#   severity: CRITICAL
#   input:
#     selector:
#       - type: yaml
package user.hookflow.compose_env_secrets

import rego.v1

deny contains res if {
	is_object(input.services)
	some name, svc in input.services
	some key, val in svc.environment
	regex.match(`(?i)(PASSWORD|PASS|SECRET|TOKEN|KEY)$`, key)
	is_string(val)
	not startswith(val, "${")
	res := sprintf("Service '%s' environment variable '%s' must use variable interpolation (${...}), not a hardcoded literal", [name, key])
}
