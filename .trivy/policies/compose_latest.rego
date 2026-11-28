# METADATA
# title: "Compose service images must not use floating latest tags"
# description: "Disallow floating latest image tags in compose.yaml."
# custom:
#   id: HF-COMPOSE-002
#   avd_id: HF-COMPOSE-002
#   severity: HIGH
#   input:
#     selector:
#       - type: yaml
package user.hookflow.compose_latest

import rego.v1

deny contains res if {
	is_object(input.services)
	some name, svc in input.services
	img := svc.image
	regex.match(`:latest(@|$)`, img)
	res := sprintf("Service '%s' image '%s' must not use floating :latest tag", [name, img])
}
