package management

import "strings"

// evaluateIfMatch applies the approved representation-validator semantics.
//
//   - an absent or empty header is a missing precondition;
//   - "*" is unconditional (the resource was already located, or the caller
//     would have received 404);
//   - weak tags never match;
//   - a comma-separated list matches when any strong tag equals the current
//     strong validator;
//   - malformed syntax is rejected.
func evaluateIfMatch(header, current string) error {
	trimmed := strings.TrimSpace(header)
	if trimmed == "" {
		return errMissingIfMatch
	}
	if trimmed == "*" {
		return nil
	}
	matched := false
	for _, candidate := range strings.Split(trimmed, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return errInvalidIfMatch
		}
		if strings.HasPrefix(candidate, "W/") {
			if !quotedTag(candidate[2:]) {
				return errInvalidIfMatch
			}
			continue
		}
		if !quotedTag(candidate) {
			return errInvalidIfMatch
		}
		if candidate[1:len(candidate)-1] == current {
			matched = true
		}
	}
	if !matched {
		return errStaleIfMatch
	}
	return nil
}

func quotedTag(value string) bool {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	inner := value[1 : len(value)-1]
	return !strings.ContainsAny(inner, "\" \t")
}
