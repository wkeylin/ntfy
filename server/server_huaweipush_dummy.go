//go:build nohuaweipush

package server

import (
	"net/http"

	"heckel.io/ntfy/v2/model"
)

const (
	// HuaweiPushAvailable is a constant used to indicate that Huawei Push support is available.
	// It can be disabled with the 'nohuaweipush' build tag.
	HuaweiPushAvailable = false
)

func (s *Server) handleHuaweiPushUpdate(w http.ResponseWriter, r *http.Request, v *visitor) error {
	return errHTTPNotFound
}

func (s *Server) handleHuaweiPushDelete(w http.ResponseWriter, r *http.Request, v *visitor) error {
	return errHTTPNotFound
}

func (s *Server) sendToHuaweiPush(v *visitor, m *model.Message) {
	// Nothing to see here
}

func (s *Server) pruneHuaweiPushSubscriptions() {
	// Nothing to see here
}
