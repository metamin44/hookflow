# METADATA
# title: "Compose published ports must bind explicitly to loopback"
# description: "Local development Compose ports must bind explicitly to 127.0.0.1 or loopback, not all interfaces."
# custom:
#   id: HF-COMPOSE-005
#   avd_id: HF-COMPOSE-005
#   severity: HIGH
#   input:
#     selector:
#       - type: yaml
package user.hookflow.compose_loopback_ports

import rego.v1

deny contains res if {
	is_object(input.services)
	some name, svc in input.services
	some port in svc.ports
	is_string(port)
	not startswith(port, "127.0.0.1:")
	not startswith(port, "[::1]:")
	res := sprintf("Service '%s' published port '%s' must bind explicitly to loopback (127.0.0.1 or [::1])", [name, port])
}
