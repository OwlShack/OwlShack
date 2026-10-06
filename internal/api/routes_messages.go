package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OwlShack/OwlShack/internal/store"
)

func (s *Server) handleAddChannel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	mutator := s.ChannelMutator()
	if mutator == nil {
		writeError(w, http.StatusServiceUnavailable, "channel management not available")
		return
	}

	adder, _, ok := mutator(name)
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var body struct {
		Name       string `json:"name"`
		PrivateKey string `json:"privateKey,omitempty"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "channel name is required")
		return
	}

	if err := adder(body.Name, body.PrivateKey); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if persist := s.ConfigPersist(); persist != nil {
		if err := persist(r.Context()); err != nil {
			s.log.Error("config persist failed after channel add", "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRemoveChannel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	channelName := r.PathValue("channel")

	mutator := s.ChannelMutator()
	if mutator == nil {
		writeError(w, http.StatusServiceUnavailable, "channel management not available")
		return
	}

	_, remover, ok := mutator(name)
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	if err := remover(channelName); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if persist := s.ConfigPersist(); persist != nil {
		if err := persist(r.Context()); err != nil {
			s.log.Error("config persist failed after channel remove", "error", err)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	provider := s.companionProvider()

	if provider == nil {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	for _, c := range provider() {
		if c.Name == name {
			writeJSON(w, http.StatusOK, c.Channels)
			return
		}
	}

	writeError(w, http.StatusNotFound, "companion not found")
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	channel := r.URL.Query().Get("channel")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	afterID, _ := strconv.ParseInt(r.URL.Query().Get("afterId"), 10, 64)
	beforeID, _ := strconv.ParseInt(r.URL.Query().Get("beforeId"), 10, 64)

	var msgs []store.Message
	var err error

	switch {
	case channel != "" && afterID > 0:
		msgs, err = s.store.Messages.ListAfter(r.Context(), cid, channel, afterID, limit)
	case channel != "" && beforeID > 0:
		msgs, err = s.store.Messages.ListBefore(r.Context(), cid, channel, beforeID, limit)
	case channel != "":
		msgs, err = s.store.Messages.List(r.Context(), cid, channel, limit, offset)
	default:
		msgs, err = s.store.Messages.ListAll(r.Context(), cid, limit, offset)
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	messages := make([]messageJSON, 0, len(msgs))
	for _, m := range msgs {
		messages = append(messages, toMessageJSON(m))
	}

	writeJSON(w, http.StatusOK, messages)
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	lookup := s.CompanionLookup()
	if lookup == nil {
		writeError(w, http.StatusServiceUnavailable, "messaging not available")
		return
	}

	channelSender, dmSender, ok := lookup(name)
	if !ok {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	var body struct {
		Channel string `json:"channel"`
		Text    string `json:"text"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Channel == "" || body.Text == "" {
		writeError(w, http.StatusBadRequest, "channel and text are required")
		return
	}

	var sendErr error
	if strings.HasPrefix(body.Channel, "dm:") {
		pubkeyHex := strings.TrimPrefix(body.Channel, "dm:")
		if dmSender == nil {
			writeError(w, http.StatusBadRequest, "direct messaging not supported")
			return
		}
		sendErr = dmSender(pubkeyHex, body.Text)
	} else {
		sendErr = channelSender(body.Channel, body.Text)
	}

	if sendErr != nil {
		s.log.Error("channel/dm send", "error", sendErr)
		writeError(w, http.StatusInternalServerError, "send failed: "+sendErr.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type messageJSON struct {
	ID           int64    `json:"id"`
	Channel      string   `json:"channel"`
	Sender       string   `json:"sender"`
	Text         string   `json:"text"`
	Direction    string   `json:"direction"`
	Timestamp    string   `json:"timestamp"`
	ReceivedAt   string   `json:"receivedAt"`
	SNR          *float64 `json:"snr,omitempty"`
	RSSI         *int8    `json:"rssi,omitempty"`
	RepeatCount  *int     `json:"repeatCount,omitempty"`
	Hops         *int     `json:"hops,omitempty"`
	PathHashSize *int     `json:"pathHashSize,omitempty"`
	Status       *string  `json:"status,omitempty"`
}

func toMessageJSON(m store.Message) messageJSON {
	return messageJSON{
		ID:           m.ID,
		Channel:      m.Channel,
		Sender:       m.Sender,
		Text:         m.Text,
		Direction:    m.Direction,
		Timestamp:    m.Timestamp.UTC().Format(time.RFC3339),
		ReceivedAt:   m.ReceivedAt.UTC().Format(time.RFC3339),
		SNR:          m.SNR,
		RSSI:         m.RSSI,
		RepeatCount:  m.RepeatCount,
		Hops:         m.Hops,
		PathHashSize: m.PathHashSize,
		Status:       m.Status,
	}
}

func (s *Server) handleRetryMessage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	messageID, err := strconv.ParseInt(r.PathValue("messageId"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message ID")
		return
	}
	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	msg, err := s.store.Messages.GetByID(r.Context(), messageID)
	if err != nil {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}

	if msg.CompanionID != cid {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}
	if msg.Direction != "tx" {
		writeError(w, http.StatusBadRequest, "can only retry sent messages")
		return
	}
	if msg.Status == nil || *msg.Status != "failed" {
		writeError(w, http.StatusBadRequest, "can only retry failed messages")
		return
	}
	if !strings.HasPrefix(msg.Channel, "dm:") {
		writeError(w, http.StatusBadRequest, "retry only supported for direct messages")
		return
	}

	lookup := s.CompanionLookup()
	if lookup == nil {
		writeError(w, http.StatusServiceUnavailable, "messaging not available")
		return
	}

	_, dmSender, ok := lookup(name)
	if !ok || dmSender == nil {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	// dmSender creates a new row with fresh status tracking.
	var delErr error
	s.store.WriteSync(func() { delErr = s.store.Messages.Delete(r.Context(), messageID) })
	if delErr != nil {
		s.serverError(w, "failed to delete message", delErr)
		return
	}

	pubkeyHex := strings.TrimPrefix(msg.Channel, "dm:")
	if sendErr := dmSender(pubkeyHex, msg.Text); sendErr != nil {
		s.log.Error("message retry", "error", sendErr)
		writeError(w, http.StatusInternalServerError, "retry failed: "+sendErr.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
