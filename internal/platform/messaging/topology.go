package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/platform/config"
)

func quorumQueueArguments() amqp.Table {
	return amqp.Table{amqp.QueueTypeArg: amqp.QueueTypeQuorum}
}

// EnsureTopology declares the durable dispatch and dead-letter topology on a
// dedicated channel. Active declarations make type, durability, and argument
// mismatches fail closed instead of being silently accepted.
func EnsureTopology(ctx context.Context, conn *amqp.Connection, _ config.RabbitMQ) (err error) {
	if ctx == nil {
		return errors.New("rabbitmq topology context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if conn == nil || conn.IsClosed() {
		return errors.New("rabbitmq topology connection is closed")
	}

	channel, err := conn.Channel()
	if err != nil {
		return errors.New("rabbitmq topology channel open failed")
	}
	defer func() {
		if closeErr := channel.Close(); err == nil && closeErr != nil {
			err = errors.New("rabbitmq topology channel close failed")
		}
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = channel.ExchangeDeclare(DispatchExchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return topologyOperationError(ctx, "dispatch exchange declaration failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = channel.ExchangeDeclare(DeadLetterExchange, amqp.ExchangeDirect, true, false, false, false, nil); err != nil {
		return topologyOperationError(ctx, "dead-letter exchange declaration failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = channel.QueueDeclare(DispatchQueue, true, false, false, false, quorumQueueArguments()); err != nil {
		return topologyOperationError(ctx, "dispatch queue declaration failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = channel.QueueDeclare(DeadLetterQueue, true, false, false, false, quorumQueueArguments()); err != nil {
		return topologyOperationError(ctx, "dead-letter queue declaration failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = channel.QueueBind(DispatchQueue, DispatchRoutingKey, DispatchExchange, false, nil); err != nil {
		return topologyOperationError(ctx, "dispatch queue binding failed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = channel.QueueBind(DeadLetterQueue, DeadLetterRoutingKey, DeadLetterExchange, false, nil); err != nil {
		return topologyOperationError(ctx, "dead-letter queue binding failed")
	}
	return nil
}

func topologyOperationError(ctx context.Context, message string) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New(message)
}

const (
	managementTimeout          = 10 * time.Second
	maxManagementResponseBytes = 1024 * 1024
)

const (
	policyDefinitionPattern             = `^hookflow\.dispatch\.q$`
	policyDefinitionApplyTo             = "queues"
	policyDefinitionDeadLetterExchange  = "dead-letter-exchange"
	policyDefinitionDeadLetterRouting   = "dead-letter-routing-key"
	policyDefinitionDeadLetterStrategy  = "dead-letter-strategy"
	policyDefinitionDeliveryLimit       = "delivery-limit"
	policyDefinitionAtMostOnce          = "at-most-once"
	policyDefinitionDeliveryLimitNumber = 20
)

var controlledPolicyKeys = [...]string{
	policyDefinitionDeadLetterExchange,
	policyDefinitionDeadLetterRouting,
	policyDefinitionDeadLetterStrategy,
	policyDefinitionDeliveryLimit,
}

type managementPolicy struct {
	Name       string         `json:"name,omitempty"`
	Pattern    string         `json:"pattern"`
	ApplyTo    string         `json:"apply-to"`
	Definition map[string]any `json:"definition"`
	Priority   json.Number    `json:"priority"`
}

type managementQueue struct {
	Name       string         `json:"name"`
	Durable    bool           `json:"durable"`
	AutoDelete bool           `json:"auto_delete"`
	Exclusive  bool           `json:"exclusive"`
	Type       string         `json:"type"`
	Arguments  map[string]any `json:"arguments"`
}

type managementExchange struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Durable    bool           `json:"durable"`
	AutoDelete bool           `json:"auto_delete"`
	Internal   bool           `json:"internal"`
	Arguments  map[string]any `json:"arguments"`
}

type managementClient struct {
	http      *http.Client
	transport *http.Transport
	baseURL   string
	username  string
	password  string
	vhost     string
}

// EnsurePolicy creates the exact dead-letter policy when absent and verifies
// that no other queue policy competes for its controlled keys. Existing
// same-name policies are never overwritten. Queue and exchange assertions use
// the management API because AMQP declarations cannot report every argument.
func EnsurePolicy(ctx context.Context, cfg config.RabbitMQ) error {
	if ctx == nil {
		return errors.New("ensure rabbitmq policy: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	uri, err := parseRabbitURI(cfg.URI)
	if err != nil {
		return errors.New("ensure rabbitmq policy: invalid AMQP URI")
	}
	baseURL, err := parseManagementURL(cfg.ManagementURL)
	if err != nil {
		return err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	management := &managementClient{
		http: &http.Client{
			Timeout:   managementTimeout,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		transport: transport,
		baseURL:   strings.TrimSuffix(strings.TrimRight(baseURL.String(), "/"), "/api"),
		username:  uri.Username,
		password:  uri.Password,
		vhost:     uri.Vhost,
	}
	defer management.close()

	policyPath := "/api/policies/" + url.PathEscape(uri.Vhost)
	var existing managementPolicy
	status, err := management.requestJSON(ctx, http.MethodGet, policyPath+"/"+url.PathEscape(PolicyName), nil, &existing)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		return errors.New("ensure rabbitmq policy: policy lookup returned an unexpected status")
	}
	if status == http.StatusOK && !exactDeadLetterPolicy(existing) {
		return errors.New("ensure rabbitmq policy: same-name policy does not match the required definition")
	}

	var policies []managementPolicy
	listStatus, err := management.requestJSON(ctx, http.MethodGet, policyPath, nil, &policies)
	if err != nil {
		return err
	}
	if listStatus != http.StatusOK {
		return errors.New("ensure rabbitmq policy: policy list returned an unexpected status")
	}
	desiredListed := false
	for _, policy := range policies {
		if policy.Name == PolicyName {
			if desiredListed || !exactDeadLetterPolicy(policy) {
				return errors.New("ensure rabbitmq policy: same-name policy list entry is inconsistent")
			}
			desiredListed = true
			continue
		}
		conflict, err := policyControlsDeadLetter(policy)
		if err != nil {
			return errors.New("ensure rabbitmq policy: queue policy pattern is invalid")
		}
		if conflict {
			return errors.New("ensure rabbitmq policy: another policy controls a dead-letter setting")
		}
	}
	if status == http.StatusOK && !desiredListed {
		return errors.New("ensure rabbitmq policy: policy lookup and policy list are inconsistent")
	}
	if status == http.StatusNotFound {
		if desiredListed {
			return errors.New("ensure rabbitmq policy: policy lookup and policy list are inconsistent")
		}
		putStatus, err := management.requestJSON(ctx, http.MethodPut, policyPath+"/"+url.PathEscape(PolicyName), requiredDeadLetterPolicy(), nil)
		if err != nil {
			return err
		}
		if putStatus < http.StatusOK || putStatus >= http.StatusMultipleChoices {
			return errors.New("ensure rabbitmq policy: policy creation returned an unexpected status")
		}
	}

	if err := management.verifyQueues(ctx); err != nil {
		return err
	}
	return management.verifyExchanges(ctx)
}

func requiredDeadLetterPolicy() managementPolicy {
	return managementPolicy{
		Pattern: policyDefinitionPattern,
		ApplyTo: policyDefinitionApplyTo,
		Definition: map[string]any{
			policyDefinitionDeadLetterExchange: DeadLetterExchange,
			policyDefinitionDeadLetterRouting:  DeadLetterRoutingKey,
			policyDefinitionDeadLetterStrategy: policyDefinitionAtMostOnce,
			policyDefinitionDeliveryLimit:      policyDefinitionDeliveryLimitNumber,
		},
		Priority: json.Number("0"),
	}
}

func exactDeadLetterPolicy(policy managementPolicy) bool {
	if policy.Pattern != policyDefinitionPattern || policy.ApplyTo != policyDefinitionApplyTo {
		return false
	}
	priority, err := policy.Priority.Int64()
	if err != nil || priority != 0 {
		return false
	}
	if len(policy.Definition) != len(controlledPolicyKeys) {
		return false
	}
	if policy.Definition[policyDefinitionDeadLetterExchange] != DeadLetterExchange ||
		policy.Definition[policyDefinitionDeadLetterRouting] != DeadLetterRoutingKey ||
		policy.Definition[policyDefinitionDeadLetterStrategy] != policyDefinitionAtMostOnce {
		return false
	}
	limit, ok := policy.Definition[policyDefinitionDeliveryLimit].(json.Number)
	if !ok {
		return false
	}
	value, err := limit.Int64()
	return err == nil && value == policyDefinitionDeliveryLimitNumber
}

func policyControlsDeadLetter(policy managementPolicy) (bool, error) {
	if policy.ApplyTo != policyDefinitionApplyTo && policy.ApplyTo != "all" {
		return false, nil
	}
	pattern, err := regexp.Compile(policy.Pattern)
	if err != nil {
		return false, err
	}
	if !pattern.MatchString(DispatchQueue) {
		return false, nil
	}
	for _, key := range controlledPolicyKeys {
		if _, controlled := policy.Definition[key]; controlled {
			return true, nil
		}
	}
	return false, nil
}

func (client *managementClient) verifyQueues(ctx context.Context) error {
	var queues []managementQueue
	path := "/api/queues/" + url.PathEscape(client.vhost)
	status, err := client.requestJSON(ctx, http.MethodGet, path, nil, &queues)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return errors.New("ensure rabbitmq policy: queue lookup returned an unexpected status")
	}
	found := make(map[string]bool, 2)
	for _, queue := range queues {
		if queue.Name != DispatchQueue && queue.Name != DeadLetterQueue {
			continue
		}
		if found[queue.Name] || !queue.Durable || queue.AutoDelete || queue.Exclusive ||
			queue.Type != amqp.QueueTypeQuorum || !exactQuorumArguments(queue.Arguments) {
			return errors.New("ensure rabbitmq policy: queue topology does not match the required quorum declaration")
		}
		found[queue.Name] = true
	}
	if !found[DispatchQueue] || !found[DeadLetterQueue] {
		return errors.New("ensure rabbitmq policy: required quorum queue is missing")
	}
	return nil
}

func (client *managementClient) verifyExchanges(ctx context.Context) error {
	var exchanges []managementExchange
	path := "/api/exchanges/" + url.PathEscape(client.vhost)
	status, err := client.requestJSON(ctx, http.MethodGet, path, nil, &exchanges)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return errors.New("ensure rabbitmq policy: exchange lookup returned an unexpected status")
	}
	found := make(map[string]bool, 2)
	for _, exchange := range exchanges {
		expectedType := ""
		switch exchange.Name {
		case DispatchExchange:
			expectedType = amqp.ExchangeTopic
		case DeadLetterExchange:
			expectedType = amqp.ExchangeDirect
		default:
			continue
		}
		if found[exchange.Name] || !exchange.Durable || exchange.AutoDelete || exchange.Internal ||
			exchange.Type != expectedType || len(exchange.Arguments) != 0 {
			return errors.New("ensure rabbitmq policy: exchange topology does not match the required durable declaration")
		}
		found[exchange.Name] = true
	}
	if !found[DispatchExchange] || !found[DeadLetterExchange] {
		return errors.New("ensure rabbitmq policy: required exchange is missing")
	}
	return nil
}

func exactQuorumArguments(arguments map[string]any) bool {
	if len(arguments) != 1 {
		return false
	}
	queueType, ok := arguments[amqp.QueueTypeArg].(string)
	return ok && queueType == amqp.QueueTypeQuorum
}

func (client *managementClient) requestJSON(ctx context.Context, method, path string, body, output any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, errors.New("ensure rabbitmq policy: encode management request failed")
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return 0, errors.New("ensure rabbitmq policy: create management request failed")
	}
	request.SetBasicAuth(client.username, client.password)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("ensure rabbitmq policy: management request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementResponseBytes+1))
		if response.StatusCode == http.StatusNotFound {
			return response.StatusCode, nil
		}
		return 0, errors.New("ensure rabbitmq policy: management API returned a non-success status")
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxManagementResponseBytes+1))
		return response.StatusCode, nil
	}

	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxManagementResponseBytes+1))
	if err != nil {
		return 0, errors.New("ensure rabbitmq policy: read management response failed")
	}
	if len(encoded) > maxManagementResponseBytes {
		return 0, errors.New("ensure rabbitmq policy: management response is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return 0, errors.New("ensure rabbitmq policy: management response is not valid JSON")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return 0, errors.New("ensure rabbitmq policy: management response must contain one JSON value")
	}
	return response.StatusCode, nil
}

func (client *managementClient) close() {
	client.transport.CloseIdleConnections()
}
