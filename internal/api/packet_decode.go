package api

import (
	"fmt"

	meshcore "github.com/meshcore-go/meshcore-go"
)

type ChannelInfo struct {
	Name string `json:"name"`
	PSK  []byte `json:"-"`
}

type ChannelLookup func(hash byte) *ChannelInfo

func PacketSummary(pkt *meshcore.Packet, channels ChannelLookup) string {
	switch pkt.PayloadType() {
	case meshcore.PayloadTypeAdvert:
		adv, err := meshcore.AdvertFromBytes(pkt.Payload)
		if err != nil {
			return fmt.Sprintf("Advert (corrupt: %v)", err)
		}
		ad := adv.AppData()
		if ad.Name != "" {
			return fmt.Sprintf("%s (%s)", ad.Name, ad.Type)
		}
		return "Advert"
	case meshcore.PayloadTypeAck:
		ack, err := meshcore.AckFromBytes(pkt.Payload)
		if err != nil {
			return "ACK"
		}
		return fmt.Sprintf("ACK %08x", ack.CRC())
	case meshcore.PayloadTypeTxtMsg:
		return "Text Message (encrypted)"
	case meshcore.PayloadTypeGrpTxt:
		gt, err := meshcore.GroupTextFromBytes(pkt.Payload)
		if err != nil {
			return "Channel Message"
		}
		if channels != nil {
			if ch := channels(gt.ChannelHash); ch != nil {
				if payload, err := gt.DecryptStruct(ch.PSK); err == nil {
					if payload.Sender != "" {
						return fmt.Sprintf("[%s] %s: %s", ch.Name, payload.Sender, payload.Text)
					}
					return fmt.Sprintf("[%s] %s", ch.Name, payload.Text)
				}
			}
		}
		return fmt.Sprintf("Channel Message (ch:%02x)", gt.ChannelHash)
	case meshcore.PayloadTypeGrpData:
		return "Channel Data"
	case meshcore.PayloadTypeTrace:
		tr, err := meshcore.TraceFromBytes(pkt.Payload)
		if err != nil {
			return "Trace"
		}
		return fmt.Sprintf("Trace %08x", tr.Tag)
	case meshcore.PayloadTypeControl:
		ctrl, err := meshcore.ControlFromBytes(pkt.Payload)
		if err != nil {
			return "Control"
		}
		switch ctrl.SubType() {
		case meshcore.ControlSubTypeDiscoverReq:
			return "Control: Discover Request"
		case meshcore.ControlSubTypeDiscoverResp:
			return "Control: Discover Response"
		default:
			return fmt.Sprintf("Control (flags:%02x)", ctrl.Flags)
		}
	case meshcore.PayloadTypeReq:
		return "Request (encrypted)"
	case meshcore.PayloadTypeResponse:
		return "Response (encrypted)"
	case meshcore.PayloadTypeAnonReq:
		return "Anonymous Request"
	case meshcore.PayloadTypePath:
		return fmt.Sprintf("Path (%d bytes)", len(pkt.Payload))
	case meshcore.PayloadTypeMultiPart:
		return "Multi-Part Fragment"
	case meshcore.PayloadTypeRawCustom:
		return fmt.Sprintf("Raw Custom (%d bytes)", len(pkt.Payload))
	default:
		return fmt.Sprintf("Unknown type %d (%d bytes)", pkt.PayloadType(), len(pkt.Payload))
	}
}
