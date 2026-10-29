package onvif

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Event is one ONVIF notification.
type Event struct {
	Topic string
	UTC   time.Time
	Data  []Item
}

// Item is a SimpleItem in an ONVIF message.
type Item struct {
	Name  string
	Value string
}

type Subscription struct {
	ID        string
	Terminate time.Time
	ch        chan Event
}

// EventHub implements the PullPoint side of the ONVIF event service.
type EventHub struct {
	mu   sync.Mutex
	subs map[string]*Subscription
}

func NewEventHub() *EventHub {
	return &EventHub{subs: make(map[string]*Subscription)}
}

func (h *EventHub) Create(ttl time.Duration) (*Subscription, time.Time) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if ttl > time.Hour {
		ttl = time.Hour
	}
	sub := &Subscription{
		ID:        uuid.NewString(),
		Terminate: time.Now().Add(ttl),
		ch:        make(chan Event, 64),
	}

	h.mu.Lock()
	h.cleanupLocked()
	h.subs[sub.ID] = sub
	h.mu.Unlock()

	return sub, sub.Terminate
}

func (h *EventHub) Remove(id string) {
	h.mu.Lock()
	delete(h.subs, id)
	h.mu.Unlock()
}

func (h *EventHub) Renew(id string, ttl time.Duration) (time.Time, bool) {
	if ttl <= 0 {
		ttl = time.Minute
	}
	if ttl > time.Hour {
		ttl = time.Hour
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sub, ok := h.subs[id]
	if !ok {
		return time.Time{}, false
	}
	sub.Terminate = time.Now().Add(ttl)
	return sub.Terminate, true
}

// Latest returns the most recently created subscription.
func (h *EventHub) Latest() (*Subscription, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked()
	var latest *Subscription
	for _, sub := range h.subs {
		if latest == nil || sub.Terminate.After(latest.Terminate) {
			latest = sub
		}
	}
	return latest, latest != nil
}

func (h *EventHub) Get(id string) (*Subscription, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub, ok := h.subs[id]
	if !ok {
		return nil, false
	}
	if time.Now().After(sub.Terminate) {
		delete(h.subs, id)
		return nil, false
	}
	return sub, true
}

func (h *EventHub) cleanupLocked() {
	now := time.Now()
	for id, sub := range h.subs {
		if now.After(sub.Terminate) {
			delete(h.subs, id)
		}
	}
}

func (h *EventHub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked()
	for _, sub := range h.subs {
		select {
		case sub.ch <- ev:
		default:
			// Slow consumer: drop the event rather than blocking the camera.
		}
	}
}

// PublishMotion emits both common ONVIF motion topics.
func (h *EventHub) PublishMotion(on bool) {
	value := "false"
	if on {
		value = "true"
	}
	now := time.Now().UTC()
	h.Publish(Event{
		Topic: "tns1:RuleEngine/CellMotionDetector/Motion",
		UTC:   now,
		Data:  []Item{{Name: "IsMotion", Value: value}},
	})
	h.Publish(Event{
		Topic: "tns1:VideoSource/MotionAlarm",
		UTC:   now,
		Data:  []Item{{Name: "State", Value: value}},
	})
}

// TriggerMotion emits a motion event. It is used by the menu bar and the web UI.
func (s *Server) TriggerMotion(on bool) {
	s.events.PublishMotion(on)
}

func (s *Server) createPullPointSubscription(inner []byte, r *http.Request) string {
	var op struct {
		InitialTerminationTime string `xml:"InitialTerminationTime"`
	}
	_ = xml.Unmarshal(inner, &op)

	ttl := parseISODuration(op.InitialTerminationTime, time.Minute)
	sub, terminate := s.events.Create(ttl)

	address := fmt.Sprintf("%s/onvif/events_service?sub=%s", s.baseURL(r), sub.ID)
	return fmt.Sprintf(`<tev:CreatePullPointSubscriptionResponse><tev:SubscriptionReference><wsa:Address>%s</wsa:Address></tev:SubscriptionReference><wsnt:CurrentTime>%s</wsnt:CurrentTime><wsnt:TerminationTime>%s</wsnt:TerminationTime></tev:CreatePullPointSubscriptionResponse>`,
		xmlEscape(address), time.Now().UTC().Format(time.RFC3339Nano), terminate.UTC().Format(time.RFC3339Nano))
}

func (s *Server) pullMessages(req *parsedRequest) (string, error) {
	var op struct {
		Timeout      string `xml:"Timeout"`
		MessageLimit int    `xml:"MessageLimit"`
	}
	_ = xml.Unmarshal(req.Inner, &op)

	timeout := parseISODuration(op.Timeout, 10*time.Second)
	if timeout > time.Minute {
		timeout = time.Minute
	}
	limit := op.MessageLimit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	sub := s.subscriptionFor(req.Raw)
	if sub == nil {
		return "", fmt.Errorf("no active subscription")
	}

	events := pullEvents(sub, timeout, limit)
	terminate := sub.Terminate.UTC().Format(time.RFC3339Nano)

	var b strings.Builder
	fmt.Fprintf(&b, `<tev:PullMessagesResponse><tev:CurrentTime>%s</tev:CurrentTime><tev:TerminationTime>%s</tev:TerminationTime>`,
		time.Now().UTC().Format(time.RFC3339Nano), terminate)
	for _, ev := range events {
		b.WriteString(notificationXML(ev))
	}
	b.WriteString(`</tev:PullMessagesResponse>`)
	return b.String(), nil
}

func pullEvents(sub *Subscription, timeout time.Duration, limit int) []Event {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var out []Event
	select {
	case ev := <-sub.ch:
		out = append(out, ev)
	case <-timer.C:
		return out
	}

	for len(out) < limit {
		select {
		case ev := <-sub.ch:
			out = append(out, ev)
		default:
			return out
		}
	}
	return out
}

func (s *Server) subscriptionFor(r *http.Request) *Subscription {
	if id := r.URL.Query().Get("sub"); id != "" {
		if sub, ok := s.events.Get(id); ok {
			return sub
		}
	}
	if sub, ok := s.events.Latest(); ok {
		return sub
	}
	return nil
}

func notificationXML(ev Event) string {
	var b strings.Builder
	b.WriteString(`<wsnt:NotificationMessage>`)
	fmt.Fprintf(&b, `<wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">%s</wsnt:Topic>`, xmlEscape(ev.Topic))
	b.WriteString(`<wsnt:Message>`)
	fmt.Fprintf(&b, `<tt:Message UtcTime="%s" PropertyOperation="Changed">`, ev.UTC.Format(time.RFC3339Nano))
	b.WriteString(`<tt:Source><tt:SimpleItem Name="VideoSourceConfigurationToken" Value="vsc_main"/><tt:SimpleItem Name="VideoSourceToken" Value="vs_main"/></tt:Source>`)
	b.WriteString(`<tt:Data>`)
	for _, item := range ev.Data {
		fmt.Fprintf(&b, `<tt:SimpleItem Name="%s" Value="%s"/>`, xmlEscape(item.Name), xmlEscape(item.Value))
	}
	b.WriteString(`</tt:Data></tt:Message>`)
	b.WriteString(`</wsnt:Message></wsnt:NotificationMessage>`)
	return b.String()
}

func (s *Server) renewSubscription(req *parsedRequest) (string, error) {
	var op struct {
		TerminationTime string `xml:"TerminationTime"`
	}
	_ = xml.Unmarshal(req.Inner, &op)

	sub := s.subscriptionFor(req.Raw)
	if sub == nil {
		return "", fmt.Errorf("no active subscription")
	}
	ttl := parseISODuration(op.TerminationTime, time.Minute)
	terminate, ok := s.events.Renew(sub.ID, ttl)
	if !ok {
		return "", fmt.Errorf("subscription expired")
	}
	return fmt.Sprintf(`<wsnt:RenewResponse><wsnt:TerminationTime>%s</wsnt:TerminationTime></wsnt:RenewResponse>`,
		terminate.UTC().Format(time.RFC3339Nano)), nil
}

func (s *Server) getEventProperties(r *http.Request) string {
	return `<tev:GetEventPropertiesResponse>` +
		`<tev:TopicNamespaceLocation>http://www.onvif.org/onvif/ver10/topics/topicns.xml</tev:TopicNamespaceLocation>` +
		`<wsnt:TopicSet>` +
		`<wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet" Name="tns1:RuleEngine/CellMotionDetector/Motion"/>` +
		`<wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet" Name="tns1:VideoSource/MotionAlarm"/>` +
		`</wsnt:TopicSet>` +
		`<tev:TopicExpressionDialect>http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet</tev:TopicExpressionDialect>` +
		`<tev:MessageContentFilterDialect>http://www.onvif.org/ver10/tev/messageContentFilter/ItemFilter</tev:MessageContentFilterDialect>` +
		`<tev:ProducerPropertiesFilterDialect>http://www.onvif.org/ver10/tev/producerPropertiesFilter/ConcreteSet</tev:ProducerPropertiesFilterDialect>` +
		`<tev:MessageContentSchemaLocation>http://www.onvif.org/onvif/ver10/schema/onvif.xsd</tev:MessageContentSchemaLocation>` +
		`</tev:GetEventPropertiesResponse>`
}

func parseISODuration(s string, def time.Duration) time.Duration {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return def
	}
	if strings.HasPrefix(s, "P") && !strings.HasPrefix(s, "PT") {
		s = strings.TrimPrefix(s, "P")
		if strings.HasSuffix(s, "D") {
			if n, err := strconv.Atoi(strings.TrimSuffix(s, "D")); err == nil {
				return time.Duration(n) * 24 * time.Hour
			}
		}
		return def
	}
	s = strings.TrimPrefix(s, "PT")
	var total time.Duration
	var num strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num.WriteRune(r)
		case r == 'H' || r == 'M' || r == 'S':
			n, err := strconv.Atoi(num.String())
			if err != nil {
				return def
			}
			num.Reset()
			switch r {
			case 'H':
				total += time.Duration(n) * time.Hour
			case 'M':
				total += time.Duration(n) * time.Minute
			case 'S':
				total += time.Duration(n) * time.Second
			}
		default:
			return def
		}
	}
	if total <= 0 {
		return def
	}
	return total
}
