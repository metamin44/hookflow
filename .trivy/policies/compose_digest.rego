# METADATA
# title: "Compose service images must be pinned by immutable digest"
# description: "Every service image in compose.yaml must include an immutable @sha256 digest to prevent mutable tag drift."
# custom:
#   id: HF-COMPOSE-001
#   avd_id: HF-COMPOSE-001
#   severity: HIGH
#   input:
#     selector:
#       - type: yaml
package user.hookflow.compose_digest

import rego.v1

deny contains res if {
	is_object(input.services)
	some name, svc in input.services
	img := svc.image
	not contains(img, "@sha256:")
	res := sprintf("Service '%s' image '%s' must be pinned by an immutable @sha256: digest", [name, img])
}
