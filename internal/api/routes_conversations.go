package api

import (
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OwlShack/OwlShack/internal/store"
)

type conversationJSON struct {
	ID            string               `json:"id"`
	Type          string               `json:"type"`
	Name          string               `json:"name"`
	Channel       string               `json:"channel"`
	LastMessage   *conversationMsgJSON `json:"lastMessage,omitempty"`
	UnreadCount   int                  `json:"unreadCount"`
	LastActive    string               `json:"lastActive,omitempty"`
	LastMessageID int64                `json:"lastMessageId,omitempty"`
	PeerType      string               `json:"peerType,omitempty"`
	IsRepeater    bool                 `json:"isRepeater,omitempty"`
	PubKey        string               `json:"pubkey,omitempty"`
}

type conversationMsgJSON struct {
	Text      string `json:"text"`
	Sender    string `json:"sender"`
	Direction string `json:"direction"`
	Timestamp string `json:"timestamp"`
}

func (s *Server) handleListConversations(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	provider := s.companionProvider()

	if provider == nil {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	found := false
	var channelNames []string
	for _, c := range provider() {
		if c.Name == name {
			found = true
			for _, ch := range c.Channels {
				channelNames = append(channelNames, ch.Name)
			}
			break
		}
	}

	if !found {
		writeError(w, http.StatusNotFound, "companion not found")
		return
	}

	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	contacts, err := s.store.Contacts.List(r.Context(), cid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list contacts")
		return
	}

	contactInfos := make([]store.ContactInfo, 0, len(contacts))
	contactMeta := make(map[string]store.ContactMetadata)
	peerTypes := make(map[string]string)
	for _, ct := range contacts {
		pubHex := hex.EncodeToString(ct.PeerPubKey)
		peerName := pubHex[:12] + "…"
		if peer, err := s.store.Peers.GetByPubKey(r.Context(), ct.PeerPubKey); err == nil && peer != nil {
			if peer.Name != "" {
				peerName = peer.Name
			}
			peerTypes[pubHex] = peer.Type
		}
		contactInfos = append(contactInfos, store.ContactInfo{
			PubKeyHex: pubHex,
			Name:      peerName,
		})
		contactMeta[pubHex] = ct.Metadata
	}

	convos, err := s.store.Conversations.List(r.Context(), cid, channelNames, contactInfos)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list conversations")
		return
	}

	result := make([]conversationJSON, 0, len(convos))
	for _, c := range convos {
		channel := c.Name
		pubkey := ""
		if c.Type == "contact" {
			pubkey = strings.TrimPrefix(c.ID, "contact:")
			channel = "dm:" + pubkey
		}
		cj := conversationJSON{
			ID:            c.ID,
			Type:          c.Type,
			Name:          c.Name,
			Channel:       channel,
			UnreadCount:   c.UnreadCount,
			PubKey:        pubkey,
			LastMessageID: c.LastMessageID,
		}
		if pubkey != "" {
			cj.PeerType = peerTypes[pubkey]
			if meta, ok := contactMeta[pubkey]; ok {
				cj.IsRepeater = meta.IsRepeater || cj.PeerType == "REPEATER"
			} else {
				cj.IsRepeater = cj.PeerType == "REPEATER"
			}
		}
		if !c.LastActive.IsZero() {
			cj.LastActive = c.LastActive.UTC().Format(time.RFC3339)
		}
		if c.LastMessage != nil {
			cj.LastMessage = &conversationMsgJSON{
				Text:      c.LastMessage.Text,
				Sender:    c.LastMessage.Sender,
				Direction: c.LastMessage.Direction,
				Timestamp: c.LastMessage.Timestamp.UTC().Format(time.RFC3339),
			}
		}
		result = append(result, cj)
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleMarkRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	conversationID := r.PathValue("conversationId")

	cid, ok := s.companionID(r.Context(), w, name)
	if !ok {
		return
	}

	var body struct {
		LastReadID int64 `json:"lastReadId"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.LastReadID <= 0 {
		lastIDStr := r.URL.Query().Get("lastReadId")
		if id, err := strconv.ParseInt(lastIDStr, 10, 64); err == nil {
			body.LastReadID = id
		}
	}
	if body.LastReadID <= 0 {
		writeError(w, http.StatusBadRequest, "lastReadId is required")
		return
	}

	var markErr error
	s.store.WriteSync(func() {
		markErr = s.store.Conversations.MarkRead(r.Context(), cid, conversationID, body.LastReadID)
	})
	if markErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark read")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
