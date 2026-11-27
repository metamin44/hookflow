package management

import (
	"net/url"
	"strconv"
	"time"

	"github.com/metamin44/hookflow/internal/modules/operations"
)

// Approved observation windows. The list is closed so a caller cannot request
// an arbitrarily wide aggregate.
var operationWindows = map[string]time.Duration{
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

const defaultOperationWindow = "24h"

var overviewQueryKeys = map[string]bool{"window": true}

var endpointHealthQueryKeys = map[string]bool{"window": true, "limit": true}

func decodeOverviewQuery(values url.Values) (time.Duration, *HTTPError) {
	if appErr := validateQueryKeys(values, overviewQueryKeys); appErr != nil {
		return 0, appErr
	}
	return decodeOperationWindow(values)
}

func decodeEndpointHealthQuery(values url.Values) (time.Duration, int, *HTTPError) {
	if appErr := validateQueryKeys(values, endpointHealthQueryKeys); appErr != nil {
		return 0, 0, appErr
	}
	window, appErr := decodeOperationWindow(values)
	if appErr != nil {
		return 0, 0, appErr
	}
	limit, appErr := decodeOperationLimit(values)
	if appErr != nil {
		return 0, 0, appErr
	}
	return window, limit, nil
}

func decodeOperationWindow(values url.Values) (time.Duration, *HTTPError) {
	if !values.Has("window") {
		return operationWindows[defaultOperationWindow], nil
	}
	window, ok := operationWindows[values.Get("window")]
	if !ok {
		return 0, fieldRejection("window", "invalid_window",
			"window must be one of 1h, 24h, 7d, 30d")
	}
	return window, nil
}

func decodeOperationLimit(values url.Values) (int, *HTTPError) {
	if !values.Has("limit") {
		return operations.DefaultEndpointLimit, nil
	}
	limit, err := strconv.Atoi(values.Get("limit"))
	if err != nil || limit < 1 || limit > operations.MaxEndpointHealth {
		return 0, fieldRejection("limit", "invalid_limit",
			"limit must be an integer between 1 and 100")
	}
	return limit, nil
}
