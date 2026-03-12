//go:build !nohuaweipush

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"heckel.io/ntfy/v2/log"
	"heckel.io/ntfy/v2/model"
	"heckel.io/ntfy/v2/user"
)

const (
	// HuaweiPushAvailable is a constant used to indicate that Huawei Push support is available.
	// It can be disabled with the 'nohuaweipush' build tag.
	HuaweiPushAvailable = true

	huaweiPushTopicSubscribeLimit = 50
)

func (s *Server) handleHuaweiPushUpdate(w http.ResponseWriter, r *http.Request, v *visitor) error {
	req, err := readJSONWithLimit[apiHuaweiPushUpdateRequest](r.Body, jsonBodyBytesLimit, false)
	if err != nil || req.PushToken == "" {
		return errHTTPBadRequestHuaweiPushSubscriptionInvalid
	} else if len(req.Topics) > huaweiPushTopicSubscribeLimit {
		return errHTTPBadRequestHuaweiPushTopicCountTooHigh
	}
	topics, err := s.topicsFromIDs(req.Topics...)
	if err != nil {
		return err
	}
	if s.userManager != nil {
		u := v.User()
		for _, t := range topics {
			if err := s.userManager.Authorize(u, t.ID, user.PermissionRead); err != nil {
				logvr(v, r).With(t).Err(err).Debug("Access to topic %s not authorized", t.ID)
				return errHTTPForbidden.With(t)
			}
		}
	}
	if err := s.huaweiPushStore.UpsertSubscription(req.PushToken, s.config.HuaweiPushProjectID, req.Topics); err != nil {
		return err
	}
	return s.writeJSON(w, newSuccessResponse())
}

func (s *Server) handleHuaweiPushDelete(w http.ResponseWriter, r *http.Request, v *visitor) error {
	req, err := readJSONWithLimit[apiHuaweiPushUpdateRequest](r.Body, jsonBodyBytesLimit, false)
	if err != nil || req.PushToken == "" {
		return errHTTPBadRequestHuaweiPushSubscriptionInvalid
	}
	if err := s.huaweiPushStore.RemoveSubscription(req.PushToken); err != nil {
		return err
	}
	return s.writeJSON(w, newSuccessResponse())
}

func (s *Server) sendToHuaweiPush(v *visitor, m *model.Message) {
	tokens, err := s.huaweiPushStore.SubscriptionsForTopic(m.Topic)
	if err != nil {
		logvm(v, m).Tag(tagHuaweiPush).Err(err).Warn("Unable to query Huawei push subscriptions")
		return
	}
	if len(tokens) == 0 {
		return
	}
	pushType, payload := toHuaweiPushMessage(m)
	log.Tag(tagHuaweiPush).With(v, m).Debug("Publishing Huawei push message to %d token(s)", len(tokens))
	result, err := s.huaweiPushClient.Send(tokens, pushType, payload)
	if err != nil {
		logvm(v, m).Tag(tagHuaweiPush).Err(err).Warn("Unable to publish Huawei push message")
		minc(metricHuaweiPushPublishedFailure)
		return
	}
	if len(result.InvalidTokens) > 0 {
		if err := s.huaweiPushStore.RemoveByTokens(result.InvalidTokens); err != nil {
			logvm(v, m).Tag(tagHuaweiPush).Err(err).Warn("Unable to remove invalid Huawei push tokens")
		}
	}
	minc(metricHuaweiPushPublishedSuccess)
}

func (s *Server) pruneHuaweiPushSubscriptions() {
	if s.huaweiPushStore == nil {
		return
	}
	removed, err := s.huaweiPushStore.ExpireSubscriptions(s.config.HuaweiPushExpiryDuration)
	if err != nil {
		log.Tag(tagHuaweiPush).Err(err).Warn("Unable to expire Huawei push subscriptions")
		return
	}
	if removed > 0 {
		log.Tag(tagHuaweiPush).Debug("Expired %d Huawei push subscription(s)", removed)
	}
}

func toHuaweiPushMessage(m *model.Message) (int, json.RawMessage) {
	switch m.Event {
	case model.MessageEvent:
		data := map[string]string{
			"id":           m.ID,
			"time":         fmt.Sprintf("%d", m.Time),
			"event":        m.Event,
			"topic":        m.Topic,
			"priority":     fmt.Sprintf("%d", m.Priority),
			"tags":         strings.Join(m.Tags, ","),
			"click":        m.Click,
			"icon":         m.Icon,
			"title":        m.Title,
			"message":      m.Message,
			"content_type": m.ContentType,
			"encoding":     m.Encoding,
		}
		if len(m.Actions) > 0 {
			actions, err := json.Marshal(m.Actions)
			if err == nil {
				data["actions"] = string(actions)
			}
		}
		if m.Attachment != nil {
			data["attachment_name"] = m.Attachment.Name
			data["attachment_type"] = m.Attachment.Type
			data["attachment_size"] = fmt.Sprintf("%d", m.Attachment.Size)
			data["attachment_expires"] = fmt.Sprintf("%d", m.Attachment.Expires)
			data["attachment_url"] = m.Attachment.URL
		}
		body := m.Message
		if len(body) > 100 {
			body = body[:97] + "..."
		}
		payload := map[string]any{
			"notification": map[string]any{
				"category": "SOCIAL_COMMUNICATION",
				"title":    m.Title,
				"body":     body,
				"data":     data,
			},
		}
		raw, _ := json.Marshal(payload)
		return 0, raw // push-type: 0 = notification
	default:
		// keepalive, poll_request, message_delete, message_clear → background message
		data := map[string]string{
			"id":    m.ID,
			"time":  fmt.Sprintf("%d", m.Time),
			"event": m.Event,
			"topic": m.Topic,
		}
		payload := map[string]any{"data": data}
		raw, _ := json.Marshal(payload)
		return 6, raw // push-type: 6 = background message
	}
}
