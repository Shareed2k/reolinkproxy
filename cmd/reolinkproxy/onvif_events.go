package main

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ONVIF Event service (ver10/events/wsdl) with WS-BaseNotification pull-point
// subscriptions. Motion transitions from the Baichuan motion listener are
// exposed as the two topics HA and most NVRs consume:
// tns1:RuleEngine/CellMotionDetector/Motion and tns1:VideoSource/MotionAlarm.

const (
	onvifEventQueueSize      = 32
	onvifEventDefaultTimeout = time.Minute
	onvifEventMaxTimeout     = time.Minute
	onvifEventDefaultTerm    = time.Minute
	onvifEventMaxTerm        = time.Hour
)

type onvifEvent struct {
	topic      string
	sourceName string
	sourceVal  string
	dataName   string
	state      bool
	operation  string // "Initialized" or "Changed"
	at         time.Time
}

type onvifEventSubscription struct {
	id      string
	expires time.Time
	events  chan onvifEvent
}

// onvifAITopics maps Baichuan AI detection classes onto the Reolink-native
// ONVIF topics Home Assistant and NVRs consume.
var onvifAITopics = map[string]string{
	"people":  "tns1:RuleEngine/MyRuleDetector/PeopleDetect",
	"vehicle": "tns1:RuleEngine/MyRuleDetector/VehicleDetect",
	"dog_cat": "tns1:RuleEngine/MyRuleDetector/DogCatDetect",
	"visitor": "tns1:RuleEngine/MyRuleDetector/Visitor",
}

type onvifEventManager struct {
	mu      sync.Mutex
	subs    map[string]*onvifEventSubscription
	last    map[string]bool            // cameraName -> last known motion state
	known   map[string]bool            // cameraName -> state ever observed
	aiLast  map[string]map[string]bool // cameraName -> AI class -> last state
	counter uint64
}

func newONVIFEventManager() *onvifEventManager {
	return &onvifEventManager{
		subs:   make(map[string]*onvifEventSubscription),
		last:   make(map[string]bool),
		known:  make(map[string]bool),
		aiLast: make(map[string]map[string]bool),
	}
}

// watchCamera consumes one camera's motion state transitions for the lifetime
// of the process and fans them out to all active subscriptions.
func (m *onvifEventManager) watchCamera(cameraName string, state *cameraMotionState) {
	if state == nil {
		return
	}
	ch, _ := state.subscribe()
	go func() {
		for snapshot := range ch {
			if snapshot.Unsupported || !snapshot.Known {
				continue
			}
			m.dispatch(cameraName, snapshot.Active, snapshot.ChangedAt)
			m.dispatchAI(cameraName, snapshot.AITypes, snapshot.ChangedAt)
		}
	}()
}

func aiEvent(cameraName, topic string, active bool, at time.Time, operation string) onvifEvent {
	return onvifEvent{
		topic:      topic,
		sourceName: "Source",
		sourceVal:  "VideoSource_" + cameraName,
		dataName:   "State",
		state:      active,
		operation:  operation,
		at:         at,
	}
}

// dispatchAI turns the set of currently-active AI classes into per-topic
// property transitions.
func (m *onvifEventManager) dispatchAI(cameraName string, aiTypes []string, at time.Time) {
	activeNow := make(map[string]bool, len(aiTypes))
	for _, aiType := range aiTypes {
		if _, known := onvifAITopics[aiType]; known {
			activeNow[aiType] = true
		}
	}

	m.mu.Lock()
	states := m.aiLast[cameraName]
	if states == nil {
		states = make(map[string]bool)
		m.aiLast[cameraName] = states
	}
	var changed []onvifEvent
	for aiType, topic := range onvifAITopics {
		next := activeNow[aiType]
		if prev, seen := states[aiType]; !seen || prev != next {
			states[aiType] = next
			changed = append(changed, aiEvent(cameraName, topic, next, at, "Changed"))
		}
	}
	subs := make([]*onvifEventSubscription, 0, len(m.subs))
	for _, sub := range m.subs {
		subs = append(subs, sub)
	}
	m.mu.Unlock()

	for _, sub := range subs {
		for _, event := range changed {
			sub.push(event)
		}
	}
}

func motionEvents(cameraName string, active bool, at time.Time, operation string) []onvifEvent {
	return []onvifEvent{
		{
			topic:      "tns1:RuleEngine/CellMotionDetector/Motion",
			sourceName: "VideoSourceConfigurationToken",
			sourceVal:  "VideoSourceConfig_" + cameraName,
			dataName:   "IsMotion",
			state:      active,
			operation:  operation,
			at:         at,
		},
		{
			topic:      "tns1:VideoSource/MotionAlarm",
			sourceName: "Source",
			sourceVal:  "VideoSource_" + cameraName,
			dataName:   "State",
			state:      active,
			operation:  operation,
			at:         at,
		},
	}
}

func (m *onvifEventManager) dispatch(cameraName string, active bool, at time.Time) {
	m.mu.Lock()
	first := !m.known[cameraName]
	unchanged := m.known[cameraName] && m.last[cameraName] == active
	m.known[cameraName] = true
	m.last[cameraName] = active
	subs := make([]*onvifEventSubscription, 0, len(m.subs))
	for _, sub := range m.subs {
		subs = append(subs, sub)
	}
	m.mu.Unlock()

	if unchanged && !first {
		return
	}

	events := motionEvents(cameraName, active, at, "Changed")
	for _, sub := range subs {
		for _, event := range events {
			sub.push(event)
		}
	}
}

func (sub *onvifEventSubscription) push(event onvifEvent) {
	for {
		select {
		case sub.events <- event:
			return
		default:
		}
		select {
		case <-sub.events: // drop the oldest to keep the queue bounded
		default:
		}
	}
}

// snapshotEvents queues the current state of every known camera into one
// subscription as Initialized property messages.
func (m *onvifEventManager) snapshotEvents(sub *onvifEventSubscription, now time.Time) {
	m.mu.Lock()
	states := make(map[string]bool, len(m.last))
	for camera, active := range m.last {
		if m.known[camera] {
			states[camera] = active
		}
	}
	m.mu.Unlock()

	for camera, active := range states {
		for _, event := range motionEvents(camera, active, now, "Initialized") {
			sub.push(event)
		}
	}
}

func (m *onvifEventManager) create(termination time.Duration, now time.Time) *onvifEventSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.counter++
	sub := &onvifEventSubscription{
		id:      strconv.FormatUint(m.counter, 10),
		expires: now.Add(termination),
		events:  make(chan onvifEvent, onvifEventQueueSize),
	}
	m.subs[sub.id] = sub
	m.purgeLocked(now)
	return sub
}

func (m *onvifEventManager) get(id string, now time.Time) *onvifEventSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked(now)
	return m.subs[id]
}

func (m *onvifEventManager) renew(id string, termination time.Duration, now time.Time) *onvifEventSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	sub := m.subs[id]
	if sub != nil {
		sub.expires = now.Add(termination)
	}
	m.purgeLocked(now)
	return sub
}

func (m *onvifEventManager) remove(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.subs[id]; !ok {
		return false
	}
	delete(m.subs, id)
	return true
}

func (m *onvifEventManager) purgeLocked(now time.Time) {
	for id, sub := range m.subs {
		if now.After(sub.expires) {
			delete(m.subs, id)
		}
	}
}

// pull drains up to limit events, long-polling until timeout when none are
// queued yet.
func (sub *onvifEventSubscription) pull(timeout time.Duration, limit int) []onvifEvent {
	var events []onvifEvent
	drain := func() {
		for len(events) < limit {
			select {
			case event := <-sub.events:
				events = append(events, event)
			default:
				return
			}
		}
	}

	drain()
	if len(events) > 0 {
		return events
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case event := <-sub.events:
		events = append(events, event)
		drain()
	case <-timer.C:
	}
	return events
}

func parseXSDuration(raw string, fallback, ceiling time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	var d time.Duration
	// Supports the PnDTnHnMnS subset ONVIF clients actually send.
	if strings.HasPrefix(raw, "PT") {
		rest := strings.TrimPrefix(raw, "PT")
		num := ""
		for _, r := range rest {
			switch {
			case r >= '0' && r <= '9' || r == '.':
				num += string(r)
			case r == 'H' || r == 'M' || r == 'S':
				v, err := strconv.ParseFloat(num, 64)
				if err != nil {
					return fallback
				}
				switch r {
				case 'H':
					d += time.Duration(v * float64(time.Hour))
				case 'M':
					d += time.Duration(v * float64(time.Minute))
				case 'S':
					d += time.Duration(v * float64(time.Second))
				}
				num = ""
			default:
				return fallback
			}
		}
	} else {
		return fallback
	}
	if d <= 0 {
		return fallback
	}
	if d > ceiling {
		return ceiling
	}
	return d
}

func (s *onvifServer) eventServiceURL(r *http.Request) string {
	path := s.cfg.EventPath
	if path == "" {
		path = "/onvif/event_service"
	}
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), path)
}

func (s *onvifServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max payload
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}
	body := string(rawBody)

	if !s.authenticate(body) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}
	if s.events == nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "event service disabled")
		return
	}

	now := time.Now().UTC()
	subID := r.URL.Query().Get("sub")

	switch action := soapAction(r, body, []string{
		"GetServiceCapabilities",
		"GetEventProperties",
		"CreatePullPointSubscription",
		"PullMessages",
		"Renew",
		"Unsubscribe",
		"SetSynchronizationPoint",
	}); action {
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<tev:GetServiceCapabilitiesResponse><tev:Capabilities WSSubscriptionPolicySupport="false" WSPullPointSupport="true" WSPausableSubscriptionManagerInterfaceSupport="false" MaxNotificationProducers="10" MaxPullPoints="10" PersistentNotificationStorage="false"/></tev:GetServiceCapabilitiesResponse>`)
	case "GetEventProperties":
		writeSOAPResponse(w, eventPropertiesResponse())
	case "CreatePullPointSubscription":
		termination := parseXSDuration(extractTokenValue(body, "InitialTerminationTime"), onvifEventDefaultTerm, onvifEventMaxTerm)
		sub := s.events.create(termination, now)
		s.events.snapshotEvents(sub, now)
		address := s.eventServiceURL(r) + "?sub=" + sub.id
		writeSOAPResponse(w, fmt.Sprintf(
			`<tev:CreatePullPointSubscriptionResponse><tev:SubscriptionReference><wsa:Address>%s</wsa:Address></tev:SubscriptionReference><wsnt:CurrentTime>%s</wsnt:CurrentTime><wsnt:TerminationTime>%s</wsnt:TerminationTime></tev:CreatePullPointSubscriptionResponse>`,
			xmlEscape(address),
			now.Format(time.RFC3339),
			sub.expires.UTC().Format(time.RFC3339),
		))
	case "PullMessages":
		sub := s.events.get(subID, now)
		if sub == nil {
			writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown or expired subscription")
			return
		}
		timeout := parseXSDuration(extractTokenValue(body, "Timeout"), onvifEventDefaultTimeout, onvifEventMaxTimeout)
		limit := 16
		if raw := extractTokenValue(body, "MessageLimit"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil && v > 0 && v < 1024 {
				limit = v
			}
		}
		events := sub.pull(timeout, limit)
		writeSOAPResponse(w, pullMessagesResponse(sub, events, time.Now().UTC()))
	case "Renew":
		termination := parseXSDuration(extractTokenValue(body, "TerminationTime"), onvifEventDefaultTerm, onvifEventMaxTerm)
		sub := s.events.renew(subID, termination, now)
		if sub == nil {
			writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown or expired subscription")
			return
		}
		writeSOAPResponse(w, fmt.Sprintf(
			`<wsnt:RenewResponse><wsnt:TerminationTime>%s</wsnt:TerminationTime><wsnt:CurrentTime>%s</wsnt:CurrentTime></wsnt:RenewResponse>`,
			sub.expires.UTC().Format(time.RFC3339),
			now.Format(time.RFC3339),
		))
	case "Unsubscribe":
		if !s.events.remove(subID) {
			writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown or expired subscription")
			return
		}
		writeSOAPResponse(w, `<wsnt:UnsubscribeResponse></wsnt:UnsubscribeResponse>`)
	case "SetSynchronizationPoint":
		sub := s.events.get(subID, now)
		if sub == nil {
			writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown or expired subscription")
			return
		}
		s.events.snapshotEvents(sub, now)
		writeSOAPResponse(w, `<tev:SetSynchronizationPointResponse></tev:SetSynchronizationPointResponse>`)
	default:
		log.Printf("onvif events: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "event action not supported")
	}
}

func eventPropertiesResponse() string {
	return `<tev:GetEventPropertiesResponse>` +
		`<tev:TopicNamespaceLocation>http://www.onvif.org/onvif/ver10/topics/topicns.xml</tev:TopicNamespaceLocation>` +
		`<wsnt:FixedTopicSet>true</wsnt:FixedTopicSet>` +
		`<wstop:TopicSet>` +
		`<tns1:RuleEngine><CellMotionDetector><Motion wstop:topic="true">` +
		`<tt:MessageDescription IsProperty="true">` +
		`<tt:Source><tt:SimpleItemDescription Name="VideoSourceConfigurationToken" Type="tt:ReferenceToken"/></tt:Source>` +
		`<tt:Data><tt:SimpleItemDescription Name="IsMotion" Type="xs:boolean"/></tt:Data>` +
		`</tt:MessageDescription>` +
		`</Motion></CellMotionDetector></tns1:RuleEngine>` +
		`<tns1:VideoSource><MotionAlarm wstop:topic="true">` +
		`<tt:MessageDescription IsProperty="true">` +
		`<tt:Source><tt:SimpleItemDescription Name="Source" Type="tt:ReferenceToken"/></tt:Source>` +
		`<tt:Data><tt:SimpleItemDescription Name="State" Type="xs:boolean"/></tt:Data>` +
		`</tt:MessageDescription>` +
		`</MotionAlarm></tns1:VideoSource>` +
		`<tns1:RuleEngine><MyRuleDetector>` +
		aiTopicDescriptionXML("PeopleDetect") +
		aiTopicDescriptionXML("VehicleDetect") +
		aiTopicDescriptionXML("DogCatDetect") +
		aiTopicDescriptionXML("Visitor") +
		`</MyRuleDetector></tns1:RuleEngine>` +
		`</wstop:TopicSet>` +
		`<wsnt:TopicExpressionDialect>http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet</wsnt:TopicExpressionDialect>` +
		`<wsnt:TopicExpressionDialect>http://docs.oasis-open.org/wsn/t-1/TopicExpression/Concrete</wsnt:TopicExpressionDialect>` +
		`<tev:MessageContentFilterDialect>http://www.onvif.org/ver10/tev/messageContentFilter/ItemFilter</tev:MessageContentFilterDialect>` +
		`<tev:MessageContentSchemaLocation>http://www.onvif.org/ver10/schema/onvif.xsd</tev:MessageContentSchemaLocation>` +
		`</tev:GetEventPropertiesResponse>`
}

func aiTopicDescriptionXML(name string) string {
	return `<` + name + ` wstop:topic="true">` +
		`<tt:MessageDescription IsProperty="true">` +
		`<tt:Source><tt:SimpleItemDescription Name="Source" Type="tt:ReferenceToken"/></tt:Source>` +
		`<tt:Data><tt:SimpleItemDescription Name="State" Type="xs:boolean"/></tt:Data>` +
		`</tt:MessageDescription>` +
		`</` + name + `>`
}

func pullMessagesResponse(sub *onvifEventSubscription, events []onvifEvent, now time.Time) string {
	var b strings.Builder
	b.WriteString(`<tev:PullMessagesResponse>`)
	fmt.Fprintf(&b, `<tev:CurrentTime>%s</tev:CurrentTime>`, now.Format(time.RFC3339))
	fmt.Fprintf(&b, `<tev:TerminationTime>%s</tev:TerminationTime>`, sub.expires.UTC().Format(time.RFC3339))
	for _, event := range events {
		fmt.Fprintf(&b,
			`<wsnt:NotificationMessage><wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">%s</wsnt:Topic><wsnt:Message><tt:Message UtcTime="%s" PropertyOperation="%s"><tt:Source><tt:SimpleItem Name="%s" Value="%s"/></tt:Source><tt:Data><tt:SimpleItem Name="%s" Value="%t"/></tt:Data></tt:Message></wsnt:Message></wsnt:NotificationMessage>`,
			event.topic,
			event.at.UTC().Format(time.RFC3339),
			event.operation,
			event.sourceName,
			xmlEscape(event.sourceVal),
			event.dataName,
			event.state,
		)
	}
	b.WriteString(`</tev:PullMessagesResponse>`)
	return b.String()
}
