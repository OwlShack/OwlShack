package companionapp

import (
	"encoding/binary"

	meshcore "github.com/OwlShack/meshcore-go"
	mc "github.com/OwlShack/meshcore-go/companion"
	"github.com/OwlShack/meshcore-go/node"
)

// The frames below are laid out as the firmware's companion_radio MyMesh.cpp writes them.

// appVersionCode is the FIRMWARE_VER_CODE whose features the app may use here: anon requests to non-contacts, not RUN_CLI_COMMAND.
const appVersionCode = 13

// maxChannels is the slots the app reads with GET_CHANNEL: the node's.
const maxChannels = node.DefaultMaxChannels

// maxContactsHalf is MAX_CONTACTS/2 as one byte; contacts here have no limit, so it reports the largest.
const maxContactsHalf = 255

func le16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// fixed is the firmware's strzcpy into n bytes: truncated to n-1 and zero-filled.
func fixed(b []byte, s string, n int) []byte {
	out := make([]byte, n)
	copy(out[:n-1], s)
	return append(b, out...)
}

func okFrame() []byte           { return []byte{mc.RespOk} }
func errFrame(code byte) []byte { return []byte{mc.RespErr, code} }
func disabledFrame() []byte     { return []byte{mc.RespDisabled} }
func noMoreFrame() []byte       { return []byte{mc.RespNoMoreMessages} }
func msgWaitingFrame() []byte   { return []byte{mc.PushMsgWaiting} }
func currTimeFrame(t uint32) []byte {
	return le32([]byte{mc.RespCurrTime}, t)
}

func sentFrame(flood bool, tag, timeoutMs uint32) []byte {
	b := []byte{mc.RespSent, 0}
	if flood {
		b[1] = 1
	}
	return le32(le32(b, tag), timeoutMs)
}

type deviceInfo struct {
	buildDate, model, version string
	pathHashMode              byte
}

func deviceInfoFrame(d deviceInfo) []byte {
	b := []byte{mc.RespDeviceInfo, appVersionCode, maxContactsHalf, maxChannels}
	b = le32(b, 0) // the BLE pin, which is never anyone's business over TCP
	b = fixed(b, d.buildDate, 12)
	b = fixed(b, d.model, 40)
	b = fixed(b, d.version, 20)
	return append(b, 0, d.pathHashMode) // a companion here never repeats
}

type selfInfo struct {
	txPower, maxTxPower int8
	pubKey              [32]byte
	lat, lon            int32
	telemetryModes      byte
	freqKHz, bwHz       uint32
	sf, cr              byte
	name                string
	shareLocation       bool
}

func selfInfoFrame(s selfInfo) []byte {
	b := []byte{mc.RespSelfInfo, meshcore.AdvertTypeChat, byte(s.txPower), byte(s.maxTxPower)}
	b = append(b, s.pubKey[:]...)
	b = le32(le32(b, uint32(s.lat)), uint32(s.lon))
	// multi_acks 0, advert_loc_policy, the telemetry modes, manual_add_contacts 1.
	var loc byte
	if s.shareLocation {
		loc = 1
	}
	b = append(b, 0, loc, s.telemetryModes, 1)
	b = le32(le32(b, s.freqKHz), s.bwHz)
	b = append(b, s.sf, s.cr)
	return append(b, s.name...)
}

type contactInfo struct {
	pubKey     [32]byte
	advType    byte
	flags      byte
	outPathLen byte
	outPath    []byte
	name       string
	lastAdvert uint32
	lat, lon   int32
	lastmod    uint32
}

// contactFrame is RESP_CODE_CONTACT, or PUSH_CODE_NEW_ADVERT with that code.
func contactFrame(code byte, c contactInfo) []byte {
	b := append([]byte{code}, c.pubKey[:]...)
	b = append(b, c.advType, c.flags, c.outPathLen)
	var path [64]byte
	copy(path[:], c.outPath)
	b = append(b, path[:]...)
	b = fixed(b, c.name, 32)
	b = le32(b, c.lastAdvert)
	b = le32(le32(b, uint32(c.lat)), uint32(c.lon))
	return le32(b, c.lastmod)
}

func channelInfoFrame(idx byte, name string, secret []byte) []byte {
	b := fixed([]byte{mc.RespChannelInfo, idx}, name, 32)
	var key [16]byte
	copy(key[:], secret)
	return append(b, key[:]...)
}

// pathLenByte is the firmware's path_len for a received packet: 0xFF when it came direct.
func pathLenByte(pkt *meshcore.Packet) byte {
	if pkt.IsRouteFlood() {
		return pkt.PathLength
	}
	return 0xFF
}

func snrByte(pkt *meshcore.Packet) byte { return byte(meshcore.SNRToWire(pkt.SNR)) }

// contactMsgFrame is RESP_CODE_CONTACT_MSG_RECV_V3; signer is a room post's author prefix.
func contactMsgFrame(snr, pathLen byte, from [32]byte, txtType byte, ts uint32, signer []byte, text string) []byte {
	b := []byte{mc.RespContactMsgRecvV3, snr, 0, 0}
	b = append(b, from[:6]...)
	b = append(b, pathLen, txtType)
	b = le32(b, ts)
	b = append(b, signer...)
	return appendText(b, text)
}

// channelMsgFrame is RESP_CODE_CHANNEL_MSG_RECV_V3.
func channelMsgFrame(snr, slot, pathLen byte, ts uint32, text string) []byte {
	b := []byte{mc.RespChannelMsgRecvV3, snr, 0, 0, slot, pathLen, meshcore.TxtTypePlain}
	b = le32(b, ts)
	return appendText(b, text)
}

// appendText cuts the text to fit the frame, as the firmware does.
func appendText(b []byte, text string) []byte {
	if room := mc.MaxFrameSize - len(b); len(text) > room {
		text = text[:room]
	}
	return append(b, text...)
}

// legacyMsgFrame turns a V3 message frame into the pre-v3 one an older app asked for.
func legacyMsgFrame(v3 []byte) []byte {
	code := byte(mc.RespContactMsgRecv)
	if v3[0] == mc.RespChannelMsgRecvV3 {
		code = mc.RespChannelMsgRecv
	}
	return append([]byte{code}, v3[4:]...)
}

func pubkeyPush(code byte, pub [32]byte) []byte { return append([]byte{code}, pub[:]...) }

func sendConfirmedFrame(ack, tripMs uint32) []byte {
	return le32(le32([]byte{mc.PushSendConfirmed}, ack), tripMs)
}

// respServerLoginOK is the firmware's RESP_SERVER_LOGIN_OK.
const respServerLoginOK = 0

// loginFrame is the firmware's answer to a login reply, its tag first; a reply it cannot read is a failure.
func loginFrame(pub [32]byte, reply []byte) []byte {
	at := func(i int) byte {
		if i < len(reply) {
			return reply[i]
		}
		return 0
	}
	switch {
	case len(reply) >= 6 && string(reply[4:6]) == "OK":
		return append([]byte{mc.PushLoginSuccess, 0}, pub[:6]...)
	case len(reply) >= 5 && reply[4] == respServerLoginOK:
		b := append([]byte{mc.PushLoginSuccess, at(6)}, pub[:6]...)
		b = append(b, reply[:4]...)
		return append(b, at(7), at(12))
	}
	return append([]byte{mc.PushLoginFail, 0}, pub[:6]...)
}

// replyPush is a STATUS or TELEMETRY response: the reply after its tag, behind the sender's prefix.
func replyPush(code byte, pub [32]byte, data []byte) []byte {
	b := append([]byte{code, 0}, pub[:6]...)
	return append(b, data...)
}

func binaryPush(tag uint32, data []byte) []byte {
	return append(le32([]byte{mc.PushBinaryResponse, 0}, tag), data...)
}

func pathDiscoveryPush(pub [32]byte, outLen byte, out []byte, inLen byte, in []byte) []byte {
	b := append([]byte{mc.PushPathDiscoveryResponse, 0}, pub[:6]...)
	b = append(append(b, outLen), out...)
	return append(append(b, inLen), in...)
}

func logRxFrame(raw []byte, snr float32, rssi int8) []byte {
	return append([]byte{mc.PushLogRxData, byte(meshcore.SNRToWire(snr)), byte(rssi)}, raw...)
}

func traceFrame(pkt *meshcore.Packet, tr *meshcore.Trace) []byte {
	b := []byte{mc.PushTraceData, 0, byte(len(tr.PathHashes)), tr.Flags}
	b = le32(le32(b, tr.Tag), tr.AuthCode)
	b = append(b, tr.PathHashes...)
	b = append(b, pkt.Path...)
	return append(b, snrByte(pkt))
}

func controlFrame(pkt *meshcore.Packet) []byte {
	b := []byte{mc.PushControlData, snrByte(pkt), byte(pkt.RSSI), pkt.PathLength}
	return append(b, pkt.Payload...)
}
